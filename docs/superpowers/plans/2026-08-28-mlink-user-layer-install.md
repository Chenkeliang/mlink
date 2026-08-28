# MLink User-Layer Installation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the guided installer, Broker, SQLite Journal, Codex/Pi/Hermes adapters, reversible configuration management, and TUI needed to connect the three local Agents to the already validated TencentDB Provider.

**Architecture:** A single Go `mlink` binary owns configuration, installation transactions, the user-level Broker, and the existing out-of-process Provider Host. Codex Hooks and the Pi Extension call the Broker over a user-only Unix socket; the Hermes Memory Provider in OrbStack calls the same API over a token-authenticated host bridge. SQLite is a durable event and installation journal, not a semantic memory backend.

**Tech Stack:** Go 1.22, standard `net/http`, `gopkg.in/yaml.v3 v3.0.1`, `modernc.org/sqlite v1.34.5`, Bubble Tea `v1.1.2`, Bubbles `v0.20.0`, Lip Gloss `v1.0.0`, macOS Keychain through `/usr/bin/security`, macOS LaunchAgent, OrbStack `orb` CLI, generated TypeScript Pi Extension, generated Python Hermes Provider.

**Spec:** `docs/superpowers/specs/2026-08-28-mlink-user-layer-install-design.md`

## Global Constraints

- Work only on the existing production-base feature branch; never implement directly on `main`, `master`, `develop`, `development`, `release`, `test`, or `testing`.
- Preserve the user's existing unstaged `README.md` change and stage every implementation file explicitly.
- Every shell command in this repository starts with `rtk`.
- Core, CLI, TUI, Broker, and Provider Host remain one Go 1.22 native binary; generated Pi and Hermes adapters are thin lifecycle shims only.
- Do not proxy or change Agent LLM traffic, model provider, Base URL, API key, subscription, login, or authentication flow.
- Use only TencentDB MemoryCore, Codex, Pi, and Hermes in this plan.
- Do not implement Mem0, package-manager distribution, Web UI, or HyMemory migration.
- Do not make MCP a prerequisite for automatic memory.
- Do not patch Hermes source. Close built-in memory only through `memory.memory_enabled=false`, `memory.user_profile_enabled=false`, and the owned `agent.disabled_toolsets` entry `memory`.
- Do not delete or migrate Hermes `MEMORY.md`, `USER.md`, HyMemory packages, or backend data.
- Use TDD: add the failing test, observe the expected failure, add the minimum implementation, and rerun the focused and package tests before each commit.
- Never touch real Codex, Pi, Hermes, LaunchAgent, Keychain, or MemoryCore state during unit and isolated integration tasks.
- The first real-machine action is a read-only ChangeSet preview. Applying it requires a fresh explicit user confirmation after the preview is shown.

---

## File and Package Map

### Existing files to modify

- `go.mod`, `go.sum`: add the exact SQLite and TUI dependencies listed above.
- `cmd/mlink/main.go`, `cmd/mlink/main_test.go`: replace the one-command dispatcher with the shared CLI entry while preserving `provider run tencentdb`.
- `internal/model/model.go`, `internal/model/model_test.go`: add adapter request, turn-fragment, and bounded-context transport models.
- `internal/provider/tencentdb/provider.go`: no semantic change; consume it through the new Broker runtime.

### New core packages

- `internal/layout`: deterministic user paths and permission creation.
- `internal/config`: versioned non-secret MLink YAML configuration and atomic persistence.
- `internal/secret`: Keychain-backed secret store with an injectable command runner.
- `internal/identity`: fixed and delegated identity resolution with HMAC-derived platform users.
- `internal/journal`: SQLite migrations, turn fragments, durable deliveries, installation ownership, and backup indexes.
- `internal/install`: ChangeSet, semantic diff, local/remote target operations, backup, apply, rollback, restore, and uninstall primitives.
- `internal/backend`: construct and supervise the existing Provider Host from an active Connection.
- `internal/broker`: identity authorization, recall/capture application service, delivery worker, HTTP/Unix transport, and context formatting.
- `internal/adapter/client`: canonical Unix-socket client shared by native hook commands.
- `internal/adapter/codex`: official Hook configuration and runtime handler.
- `internal/adapter/pi`: official Extension plan and embedded TypeScript template.
- `internal/adapter/hermes`: OrbStack target, official Memory Provider plan, YAML mutation, and embedded Python templates.
- `internal/launchagent`: render, install, load, inspect, and remove the user Broker LaunchAgent.
- `internal/doctor`: structured checks used by CLI and TUI.
- `internal/app`: one application service for detect, plan, apply, restore, uninstall, status, and doctor.
- `internal/cli`: deterministic command parsing and text/JSON rendering.
- `internal/tui`: Bubble Tea wizard and dashboard; it calls `internal/app` and owns no installation logic.

### New test fixtures

- `internal/adapter/codex/testdata`: Hook JSON and official payload goldens.
- `internal/adapter/pi/testdata`: Extension render goldens.
- `internal/adapter/hermes/testdata`: Hermes config, plugin, and fake Orb filesystem fixtures.
- `internal/install/testdata`: protected-config and rollback fixtures.
- `internal/broker/testdata`: fake Provider and adversarial request fixtures.
- `internal/e2e`: temporary-HOME installation, lifecycle, restore, uninstall, and adversarial suites.

---

### Task 1: Runtime Layout, Versioned Config, and CLI Entry

**Files:**
- Create: `internal/layout/paths.go`
- Create: `internal/layout/paths_test.go`
- Create: `internal/config/types.go`
- Create: `internal/config/store.go`
- Create: `internal/config/store_test.go`
- Create: `internal/cli/run.go`
- Create: `internal/cli/run_test.go`
- Modify: `cmd/mlink/main.go`
- Modify: `cmd/mlink/main_test.go`

**Interfaces:**
- Produces: `layout.Paths`, `layout.FromHome(home, executable string)`, `config.Config`, `config.Connection`, `config.Store.Load()`, `config.Store.SaveAtomic(Config)`, and `cli.Run(context.Context, []string, cli.Dependencies) int`.
- Preserves: `mlink provider run tencentdb` starts `server.New(tencentdb.NewServerHandler())` exactly as it does now.

- [ ] **Step 1: Write failing layout and config round-trip tests**

```go
func TestFromHomeUsesStableUserPaths(t *testing.T) {
	p, err := FromHome("/Users/test", "/tmp/mlink-build")
	if err != nil { t.Fatal(err) }
	if p.Home != "/Users/test/.mlink" { t.Fatalf("Home = %q", p.Home) }
	if p.Binary != "/Users/test/.local/bin/mlink" { t.Fatalf("Binary = %q", p.Binary) }
	if p.Socket != "/Users/test/.mlink/run/mlink.sock" { t.Fatalf("Socket = %q", p.Socket) }
}

func TestStoreRoundTripDoesNotContainSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := Store{Path: path}
	want := Config{SchemaVersion: 1, ActiveConnectionID: "local", Connections: map[string]Connection{
		"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1", SecretRefs: map[string]string{"token": "keychain://dev.mlink/connection/local/token"}},
	}}
	if err := store.SaveAtomic(want); err != nil { t.Fatal(err) }
	got, err := store.Load()
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(want, got) { t.Fatalf("round trip = %#v, want %#v", got, want) }
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("actual-secret")) { t.Fatal("secret leaked") }
}
```

- [ ] **Step 2: Run focused tests and observe missing package/type failures**

Run: `rtk go test ./internal/layout ./internal/config ./internal/cli ./cmd/mlink`

Expected: FAIL because the new packages and `cli.Run` do not exist.

- [ ] **Step 3: Implement the minimum layout, config, and command router**

Use these stable configuration types:

```go
type Config struct {
	SchemaVersion      int                   `yaml:"schema_version"`
	NamespaceID        string                `yaml:"namespace_id"`
	ActiveConnectionID string                `yaml:"active_connection_id"`
	Connections        map[string]Connection `yaml:"connections"`
	Adapters           map[string]Adapter    `yaml:"adapters"`
}

type Connection struct {
	ID                 string            `yaml:"id"`
	ProviderID         string            `yaml:"provider_id"`
	ProviderVersion    string            `yaml:"provider_version"`
	ConfigRevision     string            `yaml:"config_revision"`
	ProviderConfig     map[string]any    `yaml:"provider_config"`
	SecretRefs         map[string]string `yaml:"secret_refs"`
	TenantID           string            `yaml:"tenant_id"`
	AgentID            string            `yaml:"agent_id"`
	UserID             string            `yaml:"user_id"`
	IncludeAgentShared bool              `yaml:"include_agent_shared"`
}
```

`SaveAtomic` writes a `0600` temporary file in the destination directory, `fsync`s it, renames it, and verifies the resulting mode. `cli.Run` recognizes `provider run tencentdb`, `install`, `status`, `doctor`, `backup`, `uninstall`, `broker serve`, and `hook codex`; unimplemented public commands return exit code `2` with a stable error string during this task.

- [ ] **Step 4: Run focused and baseline tests**

Run: `rtk go test ./internal/layout ./internal/config ./internal/cli ./cmd/mlink`

Expected: PASS.

Run: `rtk go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit the foundation**

```bash
rtk git add cmd/mlink/main.go cmd/mlink/main_test.go internal/layout internal/config internal/cli
rtk git commit -m "feat: add mlink runtime configuration foundation"
```

---

### Task 2: Keychain Secrets and Stable Identity

**Files:**
- Create: `internal/secret/store.go`
- Create: `internal/secret/keychain_darwin.go`
- Create: `internal/secret/keychain_other.go`
- Create: `internal/secret/keychain_test.go`
- Create: `internal/identity/resolver.go`
- Create: `internal/identity/resolver_test.go`

**Interfaces:**
- Produces: `secret.Store` with `Put`, `Get`, and `Delete`; `identity.Resolver.ResolveFixed`; `identity.Resolver.ResolveDelegated`.
- Consumes: `config.Adapter` and `config.Connection` from Task 1.

- [ ] **Step 1: Write failing secret-runner and identity-vector tests**

```go
func TestDelegatedIdentityUsesKeyedStableHMAC(t *testing.T) {
	r := Resolver{NamespaceID: "ns-a", Key: bytes.Repeat([]byte{0x2a}, 32)}
	a, err := r.ResolveDelegated("feishu", "ou_stable")
	if err != nil { t.Fatal(err) }
	b, _ := r.ResolveDelegated("feishu", "ou_stable")
	c, _ := r.ResolveDelegated("feishu", "ou_other")
	if a != b { t.Fatalf("unstable IDs: %q != %q", a, b) }
	if a == c || !strings.HasPrefix(a, "usr_") { t.Fatalf("bad IDs: %q %q", a, c) }
}

func TestDelegatedIdentityRejectsMissingSubject(t *testing.T) {
	_, err := (Resolver{NamespaceID: "ns", Key: make([]byte, 32)}).ResolveDelegated("feishu", "")
	if !errors.Is(err, ErrIdentityMissing) { t.Fatalf("error = %v", err) }
}
```

- [ ] **Step 2: Run tests and observe missing implementations**

Run: `rtk go test ./internal/secret ./internal/identity`

Expected: FAIL because `Store` and `Resolver` are undefined.

- [ ] **Step 3: Implement the Keychain and resolver boundary**

Use this interface and never place a secret in a command argument:

```go
type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Runner interface {
	Run(context.Context, []string, io.Reader) ([]byte, error)
}
```

For `Put`, invoke `/usr/bin/security add-generic-password -U -s dev.mlink -a <account> -w` with `-w` last and provide the secret plus newline on stdin. `Get` uses `find-generic-password -s dev.mlink -a <account> -w` and returns captured bytes without logging. The non-darwin implementation returns `secret.ErrUnsupported`.

`ResolveDelegated` computes `usr_` plus the first 26 lowercase base32 characters of `HMAC-SHA256(key, namespace+":"+source+":"+subject)`. It rejects empty namespace, source, subject, or non-32-byte keys.

- [ ] **Step 4: Run tests**

Run: `rtk go test ./internal/secret ./internal/identity`

Expected: PASS, including a fake Runner assertion that the secret appears only on stdin.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/secret internal/identity
rtk git commit -m "feat: add keychain secrets and stable identity"
```

---

### Task 3: SQLite Journal and Installation Ledger

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/journal/migrations/001_initial.sql`
- Create: `internal/journal/store.go`
- Create: `internal/journal/events.go`
- Create: `internal/journal/installations.go`
- Create: `internal/journal/store_test.go`
- Create: `internal/journal/events_test.go`
- Create: `internal/journal/installations_test.go`

**Interfaces:**
- Produces: `journal.Open`, `Store.RecordFragment`, `Store.EnqueueTurn`, `Store.ClaimReady`, `Store.MarkAccepted`, `Store.MarkVisible`, `Store.MarkRetryable`, `Store.MarkPermanent`, `Store.MarkAmbiguous`, `Store.FlushSession`, `Store.RecordInstallation`, `Store.RecordBackup`, and `Store.ListBlockingEvents`.
- Consumes: `model.Turn` and `connection.RouteKey`.

- [ ] **Step 1: Add the pure-Go SQLite dependency**

Run: `rtk go get modernc.org/sqlite@v1.34.5`

Expected: `go.mod` keeps `go 1.22`; the selected module reports `GoVersion: 1.21`.

- [ ] **Step 2: Write failing migration, duplicate, conflict, and payload-clearing tests**

```go
func TestEnqueueTurnDeduplicatesAndDetectsConflict(t *testing.T) {
	store := openTestStore(t)
	first, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("turn-1", "hello"))
	if err != nil || !inserted { t.Fatalf("first = %#v %v %v", first, inserted, err) }
	second, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("turn-1", "hello"))
	if err != nil || inserted || second.ID != first.ID { t.Fatalf("duplicate = %#v %v %v", second, inserted, err) }
	_, _, err = store.EnqueueTurn(context.Background(), fixtureEnvelope("turn-1", "changed"))
	if !errors.Is(err, ErrTurnConflict) { t.Fatalf("error = %v", err) }
}

func TestMarkAcceptedClearsFinalPayload(t *testing.T) {
	store := openTestStore(t)
	event, _, _ := store.EnqueueTurn(context.Background(), fixtureEnvelope("turn-2", "sensitive"))
	if err := store.MarkAccepted(context.Background(), event.ID, model.WriteReceipt{ReceiptID: "r1", State: model.WriteAccepted}); err != nil { t.Fatal(err) }
	if got := rawPayloadForTest(t, store, event.ID); got != nil { t.Fatalf("payload retained: %q", got) }
}
```

- [ ] **Step 3: Run focused tests and observe migration/API failures**

Run: `rtk go test ./internal/journal`

Expected: FAIL because the schema and Store methods are missing.

- [ ] **Step 4: Implement migrations and state transitions**

`001_initial.sql` creates:

```sql
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE turn_fragments(
  adapter_id TEXT NOT NULL, session_id TEXT NOT NULL, turn_id TEXT NOT NULL,
  role TEXT NOT NULL, content BLOB NOT NULL, content_hash TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(adapter_id, session_id, turn_id, role)
);
CREATE TABLE journal_events(
  id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE,
  adapter_id TEXT NOT NULL, session_id TEXT NOT NULL, turn_id TEXT NOT NULL,
  connection_id TEXT NOT NULL, provider_id TEXT NOT NULL,
  provider_version TEXT NOT NULL, config_revision TEXT NOT NULL,
  tenant_id TEXT NOT NULL, agent_id TEXT NOT NULL, user_id TEXT NOT NULL,
  content_hash TEXT NOT NULL, payload BLOB, state TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT,
  receipt_json BLOB, error_code TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(adapter_id, session_id, turn_id)
);
CREATE TABLE delivery_attempts(
  id INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL,
  started_at TEXT NOT NULL, finished_at TEXT, delivery_state TEXT,
  error_code TEXT, FOREIGN KEY(event_id) REFERENCES journal_events(id)
);
CREATE TABLE adapter_installations(
  adapter_id TEXT PRIMARY KEY, agent_type TEXT NOT NULL, version TEXT NOT NULL,
  status TEXT NOT NULL, manifest_json BLOB NOT NULL, installed_at TEXT NOT NULL
);
CREATE TABLE owned_resources(
  owner_id TEXT NOT NULL, target TEXT NOT NULL, semantic_fingerprint TEXT NOT NULL,
  post_apply_hash TEXT NOT NULL, PRIMARY KEY(owner_id, target)
);
CREATE TABLE backup_artifacts(
  backup_id TEXT NOT NULL, target TEXT NOT NULL, backup_path TEXT NOT NULL,
  before_hash TEXT NOT NULL, after_hash TEXT NOT NULL, semantic_snapshot BLOB NOT NULL,
  created_at TEXT NOT NULL, PRIMARY KEY(backup_id, target)
);
```

Open with WAL, `foreign_keys=ON`, `busy_timeout=5000`, and a single writer connection. Use explicit transactions and compare-and-set state transitions. `accepted` and `visible` clear `payload`; `ambiguous` retains it for audit.

- [ ] **Step 5: Run Journal and race tests**

Run: `rtk go test ./internal/journal`

Expected: PASS.

Run: `rtk go test -race ./internal/journal`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
rtk git add go.mod go.sum internal/journal
rtk git commit -m "feat: add sqlite delivery and installation journal"
```

---

### Task 4: ChangeSet, Backups, Atomic Apply, and Rollback

**Files:**
- Create: `internal/install/types.go`
- Create: `internal/install/target.go`
- Create: `internal/install/local_target.go`
- Create: `internal/install/backup.go`
- Create: `internal/install/transaction.go`
- Create: `internal/install/render.go`
- Create: `internal/install/transaction_test.go`
- Create: `internal/install/render_test.go`
- Create: `internal/install/testdata/protected.yaml`

**Interfaces:**
- Produces: `install.ChangeSet`, `install.Operation`, `install.Target`, `install.Transaction.Apply`, `install.Transaction.Rollback`, `install.RenderText`, and `install.RenderJSON`.
- Consumes: installation and backup methods from `journal.Store`.

- [ ] **Step 1: Write failing preview-before-write and reverse-rollback tests**

```go
func TestTransactionDoesNotWriteDuringPlan(t *testing.T) {
	target := newFakeTarget(map[string][]byte{"/config": []byte("before")})
	plan, err := BuildChangeSet(target, []DesiredResource{{Target: "/config", Content: []byte("after"), Mode: 0o600}})
	if err != nil { t.Fatal(err) }
	if got := string(target.files["/config"]); got != "before" { t.Fatalf("wrote during plan: %q", got) }
	if plan.Operations[0].BeforeHash == plan.Operations[0].ProposedHash { t.Fatal("hashes should differ") }
}

func TestApplyFailureRollsBackInReverseOrder(t *testing.T) {
	target := newFailingTarget(2)
	plan := fixtureThreeOperationPlan()
	err := NewTransaction(target, newMemoryLedger()).Apply(context.Background(), plan)
	if err == nil { t.Fatal("expected failure") }
	want := []string{"apply:a", "apply:b", "rollback:a"}
	if !reflect.DeepEqual(want, target.actions) { t.Fatalf("actions = %#v, want %#v", target.actions, want) }
}
```

- [ ] **Step 2: Run focused tests and observe missing implementation failures**

Run: `rtk go test ./internal/install`

Expected: FAIL because ChangeSet and Transaction are undefined.

- [ ] **Step 3: Implement deterministic plans and reversible operations**

Use these operation contracts:

```go
type Operation struct {
	ID                  string         `json:"operation_id"`
	OwnerID             string         `json:"owner_id"`
	Target              string         `json:"target"`
	Action              Action         `json:"action"`
	BeforeHash          string         `json:"before_hash"`
	ProposedHash        string         `json:"proposed_hash"`
	SemanticDiff        []SemanticDiff `json:"semantic_diff"`
	ProtectedInvariants []Invariant    `json:"protected_invariants"`
	Content             []byte         `json:"-"`
	Mode                fs.FileMode    `json:"-"`
}

type Target interface {
	Read(context.Context, string) ([]byte, fs.FileMode, error)
	WriteAtomic(context.Context, string, []byte, fs.FileMode) error
	Remove(context.Context, string) error
	Run(context.Context, []string, io.Reader) ([]byte, error)
}
```

Before Apply, recompute every `BeforeHash`; any drift returns `install.ErrPlanStale`. Backups must hash-verify before the first write. After each operation, recompute the target hash and protected semantic invariants. Record ownership only after all runtime-neutral file checks pass.

- [ ] **Step 4: Run focused tests**

Run: `rtk go test ./internal/install`

Expected: PASS, including no-write preview, stale-plan rejection, backup hash validation, semantic invariant failure, and reverse rollback.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/install
rtk git commit -m "feat: add reversible installation transactions"
```

---

### Task 5: Connection Runtime and Broker Delivery Worker

**Files:**
- Create: `internal/provider/tencentdb/bundled.go`
- Create: `internal/provider/tencentdb/bundled_test.go`
- Create: `internal/backend/runtime.go`
- Create: `internal/backend/runtime_test.go`
- Create: `internal/broker/service.go`
- Create: `internal/broker/worker.go`
- Create: `internal/broker/service_test.go`
- Create: `internal/broker/worker_test.go`
- Modify: `internal/model/model.go`
- Modify: `internal/model/model_test.go`

**Interfaces:**
- Produces: `backend.Runtime.Start`, `backend.Runtime.Session`, `broker.Service.Recall`, `broker.Service.SubmitFragment`, `broker.Service.SubmitTurn`, `broker.Service.Flush`, and `broker.Worker.Run`.
- Consumes: `host.Start`, `secret.Store`, `identity.Resolver`, `journal.Store`, and the active `config.Connection`.

- [ ] **Step 1: Write failing route-pinning and ambiguous-delivery tests**

```go
func TestWorkerMarksMaybeSentUnsafeCaptureAmbiguous(t *testing.T) {
	store := seededJournal(t, fixtureEnvelope("turn-ambiguous", "remember"))
	provider := &fakeSession{captureErr: &host.CallError{Delivery: host.DeliveryMaybeSent, ReplaySafe: false, Code: protocol.ErrorTemporarilyUnavailable}}
	worker := Worker{Journal: store, Provider: provider, Clock: fixedClock()}
	if err := worker.DrainOne(context.Background()); err != nil { t.Fatal(err) }
	if got := eventState(t, store, "turn-ambiguous"); got != journal.StateAmbiguous { t.Fatalf("state = %q", got) }
}

func TestQueuedEventKeepsOriginalRouteRevision(t *testing.T) {
	store := seededJournal(t, fixtureEnvelopeWithRevision("turn-route", "rev-old"))
	provider := &recordingSession{}
	worker := Worker{Journal: store, Provider: provider}
	if err := worker.DrainOne(context.Background()); err != nil { t.Fatal(err) }
	if provider.route.ConfigRevision != "rev-old" { t.Fatalf("revision = %q", provider.route.ConfigRevision) }
}
```

- [ ] **Step 2: Run tests and observe missing Broker/runtime failures**

Run: `rtk go test ./internal/backend ./internal/broker ./internal/provider/tencentdb ./internal/model`

Expected: FAIL because Broker runtime and request models are undefined.

- [ ] **Step 3: Implement the bundled manifest and Broker service**

`tencentdb.BundledManifest()` returns the already validated immutable identity and capabilities for `dev.mlink.tencentdb@0.1.0`; it must remain compatible with `manifest.ResolveBundled`.

Add transport models without Provider-specific fields:

```go
type AdapterIdentity struct {
	Source        string `json:"source"`
	SourceSubject string `json:"source_subject,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
}

type TurnFragment struct {
	AdapterID string          `json:"adapter_id"`
	Identity  AdapterIdentity `json:"identity"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	OccurredAt time.Time      `json:"occurred_at"`
}
```

The worker classifies `host.CallError` as follows:

- `DeliveryNotSent` plus transient code: `retryable_failed` with bounded exponential backoff.
- `DeliveryMaybeSent` and `ReplaySafe=false`: `ambiguous` with no retry.
- authentication, configuration, invalid identity, or permanent failure: `permanent_failed`.
- successful `accepted/visible`: record receipt and clear payload.

- [ ] **Step 4: Run focused, race, and baseline tests**

Run: `rtk go test ./internal/backend ./internal/broker ./internal/provider/tencentdb ./internal/model`

Expected: PASS.

Run: `rtk go test -race ./internal/backend ./internal/broker`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/provider/tencentdb/bundled.go internal/provider/tencentdb/bundled_test.go internal/backend internal/broker internal/model/model.go internal/model/model_test.go
rtk git commit -m "feat: add broker routing and durable delivery"
```

---

### Task 6: Authenticated Unix and OrbStack Broker Transports

**Files:**
- Create: `internal/broker/auth.go`
- Create: `internal/broker/http.go`
- Create: `internal/broker/http_test.go`
- Create: `internal/broker/server.go`
- Create: `internal/broker/server_test.go`
- Create: `internal/adapter/client/client.go`
- Create: `internal/adapter/client/client_test.go`

**Interfaces:**
- Produces: `broker.Grant`, `broker.Authorizer`, `broker.Server.ServeUnix`, `broker.Server.ServeHTTP`, and `client.Client`.
- Consumes: `broker.Service` from Task 5.

- [ ] **Step 1: Write failing identity-spoof and timeout tests**

```go
func TestHermesGrantRejectsDirectCanonicalUserOverride(t *testing.T) {
	server := newTestServer(t, Grant{AdapterID: "hermes", Mode: Delegated, Source: "feishu"})
	response := postJSON(t, server.URL+"/v1/recall", "token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "source_subject": "ou_a", "user_id": "usr_forged", "query": "x",
	})
	if response.StatusCode != http.StatusForbidden { t.Fatalf("status = %d", response.StatusCode) }
}

func TestRecallTimeoutReturnsEmptyFailOpenBundle(t *testing.T) {
	server := newSlowTestServer(t, 2*time.Second)
	client := Client{SocketPath: server.Socket, RecallTimeout: 50*time.Millisecond}
	bundle, err := client.Recall(context.Background(), fixtureRecallRequest())
	if err != nil { t.Fatal(err) }
	if len(bundle.Items) != 0 || len(bundle.Warnings) == 0 { t.Fatalf("bundle = %#v", bundle) }
}
```

- [ ] **Step 2: Run tests and observe missing transport failures**

Run: `rtk go test ./internal/broker ./internal/adapter/client`

Expected: FAIL because Server, Grant, and Client do not exist.

- [ ] **Step 3: Implement one API on two transports**

Register only:

```text
GET  /v1/health
POST /v1/recall
POST /v1/turn-fragments
POST /v1/turns
POST /v1/sessions/{session_id}/flush
```

Unix-socket requests still require a known `adapter_id`; the socket directory and file are user-only. HTTP requires `Authorization: Bearer <token>` and compares a SHA-256 token digest in constant time. Fixed grants ignore caller-supplied canonical identity; delegated Hermes grants accept only the configured source and derive the canonical user in Broker.

Bound JSON bodies before decoding, reject unknown fields, and return stable error codes without stack traces or memory text. The client maps recall timeouts to an empty `ContextBundle` warning, while capture failures remain observable to the Adapter without blocking its host Agent.

- [ ] **Step 4: Run focused and race tests**

Run: `rtk go test ./internal/broker ./internal/adapter/client`

Expected: PASS.

Run: `rtk go test -race ./internal/broker ./internal/adapter/client`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/broker internal/adapter/client
rtk git commit -m "feat: expose authenticated broker transports"
```

---

### Task 7: Codex Official Hooks

**Files:**
- Create: `internal/adapter/codex/config.go`
- Create: `internal/adapter/codex/config_test.go`
- Create: `internal/adapter/codex/handler.go`
- Create: `internal/adapter/codex/handler_test.go`
- Create: `internal/adapter/codex/testdata/existing-hooks.json`
- Create: `internal/adapter/codex/testdata/user-prompt.json`
- Create: `internal/adapter/codex/testdata/stop.json`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Produces: `codex.PlanHooks`, `codex.RemoveOwnedHooks`, and `codex.Handle(eventName, stdin, stdout, client)`.
- Consumes: `install.DesiredResource` and `client.Client`.

- [ ] **Step 1: Write failing Hook merge, replay, and output-golden tests**

```go
func TestPlanHooksPreservesExistingEventsAndIsIdempotent(t *testing.T) {
	existing := readFixture(t, "existing-hooks.json")
	first, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil { t.Fatal(err) }
	second, err := PlanHooks(first, "/Users/test/.local/bin/mlink")
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(first, second) { t.Fatal("second install changed hooks") }
	assertExistingHookPresent(t, second, "PreToolUse")
	assertMLinkHookCount(t, second, 4)
}

func TestUserPromptSubmitReturnsOfficialAdditionalContext(t *testing.T) {
	client := &fakeClient{bundle: model.ContextBundle{Items: []model.ContextItem{{ID: "m1", Text: "prefers concise output"}}}}
	out := new(bytes.Buffer)
	err := Handle(context.Background(), "UserPromptSubmit", bytes.NewReader(readFixture(t, "user-prompt.json")), out, client)
	if err != nil { t.Fatal(err) }
	assertJSONPath(t, out.Bytes(), "hookSpecificOutput.hookEventName", "UserPromptSubmit")
	assertContains(t, out.String(), "untrusted historical memory")
}
```

- [ ] **Step 2: Run tests and observe failures**

Run: `rtk go test ./internal/adapter/codex ./internal/cli`

Expected: FAIL because the Codex planner and handler are missing.

- [ ] **Step 3: Implement official Hook configuration and runtime behavior**

Plan four command Hooks in `~/.codex/hooks.json` using the fixed absolute binary path:

```json
{
  "hooks": {
    "SessionStart": [{"matcher":"startup|resume|compact","hooks":[{"type":"command","command":"/Users/test/.local/bin/mlink hook codex SessionStart","timeout":2,"additionalContextLimit":1200,"statusMessage":"Loading MLink memory"}]}],
    "UserPromptSubmit": [{"hooks":[{"type":"command","command":"/Users/test/.local/bin/mlink hook codex UserPromptSubmit","timeout":2,"additionalContextLimit":1200,"statusMessage":"Recalling MLink memory"}]}],
    "Stop": [{"hooks":[{"type":"command","command":"/Users/test/.local/bin/mlink hook codex Stop","timeout":2}]}],
    "SessionEnd": [{"hooks":[{"type":"command","command":"/Users/test/.local/bin/mlink hook codex SessionEnd","timeout":3}]}]
  }
}
```

Use official `session_id`, `turn_id`, `prompt`, and `last_assistant_message`; never read `transcript_path`. Format recall as a bounded block headed `MLink untrusted historical memory`; escape no content into JSON manually—always use `encoding/json`.

Register `mlink hook codex <Event>` in `cli.Run`. Exit `0` with fail-open JSON for Broker timeout; return non-zero only for malformed official input.

- [ ] **Step 4: Run focused and baseline tests**

Run: `rtk go test ./internal/adapter/codex ./internal/cli`

Expected: PASS.

Run: `rtk go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapter/codex internal/cli
rtk git commit -m "feat: integrate codex lifecycle hooks"
```

---

### Task 8: Pi Official Extension

**Files:**
- Create: `internal/adapter/pi/plan.go`
- Create: `internal/adapter/pi/plan_test.go`
- Create: `internal/adapter/pi/templates/mlink.ts.tmpl`
- Create: `internal/adapter/pi/testdata/mlink.ts.golden`

**Interfaces:**
- Produces: `pi.PlanExtension(binaryPath, socketPath string)` and `pi.RemoveOwnedExtension`.
- Consumes: `install.DesiredResource`.

- [ ] **Step 1: Write failing deterministic-template and event-finalization tests**

```go
func TestPlanExtensionUsesOfficialSettledLifecycle(t *testing.T) {
	resource, err := PlanExtension("/Users/test/.local/bin/mlink", "/Users/test/.mlink/run/mlink.sock")
	if err != nil { t.Fatal(err) }
	text := string(resource.Content)
	for _, event := range []string{"session_start", "before_agent_start", "agent_end", "agent_settled", "session_shutdown"} {
		if !strings.Contains(text, `pi.on("`+event+`"`) { t.Fatalf("missing %s", event) }
	}
	if strings.Contains(text, "modelProvider") || strings.Contains(text, "baseURL") { t.Fatal("extension touches model config") }
}
```

- [ ] **Step 2: Run tests and observe missing template failures**

Run: `rtk go test ./internal/adapter/pi`

Expected: FAIL because PlanExtension and the template do not exist.

- [ ] **Step 3: Implement a thin Node-standard-library Extension**

The embedded TypeScript template imports only `ExtensionAPI`, `node:http`, and `node:crypto`. It calls the Unix socket with `http.request({socketPath, path, method:"POST"})`, sets a 800 ms recall timeout, and fails open.

Maintain this in-memory state:

```typescript
type PendingTurn = {
  sessionId: string
  turnId: string
  prompt: string
  assistant: string
}

let sessionId = ""
let pending: PendingTurn | null = null
```

- `session_start`: derive `sessionId` from `ctx.sessionManager.getSessionFile()` or a process UUID.
- `before_agent_start`: generate `turnId`, save `event.prompt`, recall, and return a custom message with `customType: "mlink-memory"`.
- `agent_end`: extract and replace only the latest assistant candidate from `event.messages`.
- `agent_settled`: submit the final pending turn once, then clear it.
- `session_shutdown`: request a 500 ms flush.

Never include tool calls, tool results, system prompts, files, environment variables, or images in capture.

- [ ] **Step 4: Run focused tests and compare the golden**

Run: `rtk go test ./internal/adapter/pi`

Expected: PASS and rendered output exactly matches `testdata/mlink.ts.golden`.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapter/pi
rtk git commit -m "feat: add pi memory extension"
```

---

### Task 9: Hermes Official Provider, Built-In Memory Shutdown, and Orb Target

**Files:**
- Create: `internal/adapter/hermes/orb.go`
- Create: `internal/adapter/hermes/orb_test.go`
- Create: `internal/adapter/hermes/config.go`
- Create: `internal/adapter/hermes/config_test.go`
- Create: `internal/adapter/hermes/plan.go`
- Create: `internal/adapter/hermes/plan_test.go`
- Create: `internal/adapter/hermes/templates/__init__.py.tmpl`
- Create: `internal/adapter/hermes/templates/plugin.yaml.tmpl`
- Create: `internal/adapter/hermes/testdata/config.yaml`
- Create: `internal/adapter/hermes/testdata/plugin.golden.py`

**Interfaces:**
- Produces: `hermes.OrbTarget`, `hermes.Detect`, `hermes.PlanProvider`, `hermes.MergeConfig`, and `hermes.RestoreConfig`.
- Consumes: `install.Target`, `install.DesiredResource`, and Broker HTTP grant data.

- [ ] **Step 1: Write failing config ownership and multi-user template tests**

```go
func TestMergeConfigDisablesOnlyBuiltinMemoryAndSelectsMLink(t *testing.T) {
	before := readFixture(t, "config.yaml")
	after, ownership, err := MergeConfig(before)
	if err != nil { t.Fatal(err) }
	assertYAMLValue(t, after, "memory.memory_enabled", false)
	assertYAMLValue(t, after, "memory.user_profile_enabled", false)
	assertYAMLValue(t, after, "memory.provider", "mlink")
	assertStringListContains(t, after, "agent.disabled_toolsets", "memory")
	assertYAMLSubtreeEqualExcept(t, before, after, ownership.Paths())
}

func TestPluginPrefersStableAlternateUserAndRejectsMissingIdentity(t *testing.T) {
	plugin := renderPluginForTest(t)
	if !strings.Contains(plugin, `subject = str(kwargs.get("user_id_alt") or kwargs.get("user_id") or "").strip()`) { t.Fatal("stable identity precedence missing") }
	if !strings.Contains(plugin, `if not subject:`) || !strings.Contains(plugin, `identity_missing`) { t.Fatal("missing identity rejection absent") }
}
```

- [ ] **Step 2: Run tests and observe missing Orb/config failures**

Run: `rtk go test ./internal/adapter/hermes`

Expected: FAIL because OrbTarget, MergeConfig, and templates are missing.

- [ ] **Step 3: Implement direct-argv Orb operations and official Provider files**

`OrbTarget` executes direct argv only:

```text
orb -m <machine> cat <path>
orb -m <machine> mkdir -p <dir>
orb -m <machine> tee <temporary-path>
orb -m <machine> chmod 0600 <temporary-path>
orb -m <machine> mv <temporary-path> <target-path>
orb -m <machine> rm <owned-path>
```

Do not use `sh -c`, interpolate shell code, or write outside the detected `$HERMES_HOME` and MLink-owned temporary paths.

The Python Provider uses only the Python standard library and the official `MemoryProvider` ABC. `initialize()` saves `platform`, prefers `user_id_alt` over `user_id`, loads the MLink endpoint/token from `$HERMES_HOME/mlink.json`, and creates a bounded daemon queue. `prefetch()` performs an 800 ms HTTP recall and returns an empty string on error. `sync_turn()` enqueues user/assistant text and returns immediately. `shutdown()` waits at most two seconds. `get_tool_schemas()` returns `[]`.

`MergeConfig` uses `yaml.Node`, preserves unknown keys, stores ownership for exactly:

```text
memory.memory_enabled
memory.user_profile_enabled
memory.provider
agent.disabled_toolsets[memory]
```

It never deletes `MEMORY.md`, `USER.md`, or the old Provider package.

- [ ] **Step 4: Run focused tests**

Run: `rtk go test ./internal/adapter/hermes`

Expected: PASS, including direct-argv assertions, semantic YAML preservation, repeated install idempotency, token mode `0600`, and uninstall restoration.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/adapter/hermes
rtk git commit -m "feat: add hermes mlink memory provider"
```

---

### Task 10: LaunchAgent and End-to-End Installer Orchestration

**Files:**
- Create: `internal/launchagent/launchagent.go`
- Create: `internal/launchagent/launchagent_test.go`
- Create: `internal/app/service.go`
- Create: `internal/app/detect.go`
- Create: `internal/app/install.go`
- Create: `internal/app/restore.go`
- Create: `internal/app/uninstall.go`
- Create: `internal/app/install_test.go`
- Create: `internal/app/restore_test.go`
- Create: `internal/app/uninstall_test.go`

**Interfaces:**
- Produces: `app.Service.Detect`, `app.Service.PlanInstall`, `app.Service.ApplyInstall`, `app.Service.PlanRestore`, `app.Service.ApplyRestore`, `app.Service.PlanUninstall`, and `app.Service.ApplyUninstall`.
- Consumes: Tasks 1–9 and `journal.Store` as the installation ledger.

Use this request boundary so Secret bytes never enter a serialized ChangeSet:

```go
type InstallRequest struct {
	Agents        []Agent
	Connection    config.Connection
	SecretInputs  map[string][]byte
	HermesMachine string
	HermesHome    string
}
```

`PlanInstall` validates only the presence of required Secret input and renders its Keychain reference as redacted. `ApplyInstall(ctx, planID, request)` regenerates the plan from the same non-secret fields, verifies the ID and before-hashes, then stores Secret bytes transactionally. If a later operation fails, a newly created Keychain item is deleted or a previous value is restored from process memory; Secret bytes never enter config, Journal, backup files, JSON output, or logs.

- [ ] **Step 1: Write failing full-plan, protected-config, and uninstall-block tests**

```go
func TestPlanInstallContainsAllSelectedResourcesAndNoWrites(t *testing.T) {
	fixture := newIsolatedFixture(t)
	plan, err := fixture.App.PlanInstall(context.Background(), InstallRequest{Agents: []Agent{Codex, Pi, Hermes}, ProviderID: "dev.mlink.tencentdb"})
	if err != nil { t.Fatal(err) }
	assertTargets(t, plan, fixture.CodexHooks, fixture.PiExtension, fixture.HermesConfig, fixture.HermesPlugin, fixture.LaunchAgent)
	fixture.AssertNoWrites(t)
}

func TestUninstallBlocksStateDeletionWithAmbiguousEvents(t *testing.T) {
	fixture := newInstalledFixture(t)
	fixture.SeedAmbiguousEvent(t)
	_, err := fixture.App.PlanUninstall(context.Background(), UninstallRequest{RemoveState: true})
	if !errors.Is(err, journal.ErrBlockingEvents) { t.Fatalf("error = %v", err) }
}
```

- [ ] **Step 2: Run tests and observe missing orchestration failures**

Run: `rtk go test ./internal/launchagent ./internal/app`

Expected: FAIL because the application service is missing.

- [ ] **Step 3: Implement one orchestration path shared by CLI and TUI**

`PlanInstall` performs only read-only detection and builds operations in this order:

```text
create MLink directories
copy verified binary to ~/.local/bin/mlink
write non-secret config
store the MemoryCore token in Keychain during Apply only
write Provider manifest/config references
install Codex Hooks
install Pi Extension
install Hermes Provider and semantic config changes
write LaunchAgent plist
load/restart LaunchAgent
verify Broker and selected adapters
commit installation manifest
```

File writes happen only in `ApplyInstall` after it receives the exact plan ID and all before-hashes still match. Service actions are rollback-aware: a newly loaded LaunchAgent is unloaded if a later verification fails.

Restore and uninstall reuse the ownership ledger; they never restore a whole file over user modifications unless its current hash equals the known post-install hash. Conflicts are rendered as planned blockers.

- [ ] **Step 4: Run focused and baseline tests**

Run: `rtk go test ./internal/launchagent ./internal/app`

Expected: PASS.

Run: `rtk go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/launchagent internal/app
rtk git commit -m "feat: orchestrate guided installation and rollback"
```

---

### Task 11: CLI Preview, Apply, Status, Restore, and Uninstall

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Create: `internal/cli/install.go`
- Create: `internal/cli/status.go`
- Create: `internal/cli/backup.go`
- Create: `internal/cli/uninstall.go`
- Create: `internal/cli/testdata/install-preview.golden`
- Modify: `cmd/mlink/main.go`
- Modify: `cmd/mlink/main_test.go`

**Interfaces:**
- Produces the public commands specified in the design and stable JSON/exit-code behavior.
- Consumes only `app.Service`; CLI does not call adapter planners or Journal SQL directly.

- [ ] **Step 1: Write failing CLI preview and consent tests**

```go
func TestInstallDryRunPrintsPlanAndNeverApplies(t *testing.T) {
	app := &fakeApp{plan: fixtureInstallPlan()}
	out := new(bytes.Buffer)
	code := Run(context.Background(), []string{"install", "--dry-run", "codex", "pi", "hermes"}, Dependencies{App: app, Stdout: out})
	if code != 0 { t.Fatalf("code = %d", code) }
	if app.applyCalls != 0 { t.Fatalf("apply calls = %d", app.applyCalls) }
	assertGolden(t, "install-preview.golden", out.Bytes())
}

func TestInstallDeclinedConsentNeverApplies(t *testing.T) {
	app := &fakeApp{plan: fixtureInstallPlan()}
	code := Run(context.Background(), []string{"install", "codex"}, Dependencies{App: app, Stdin: strings.NewReader("n\n"), Stdout: io.Discard})
	if code != 0 || app.applyCalls != 0 { t.Fatalf("code/apply = %d/%d", code, app.applyCalls) }
}
```

- [ ] **Step 2: Run CLI tests and observe failures**

Run: `rtk go test ./internal/cli ./cmd/mlink`

Expected: FAIL because public commands still return the Task 1 placeholder error.

- [ ] **Step 3: Implement deterministic CLI behavior**

Support:

```text
mlink install [codex] [pi] [hermes] [--dry-run] [--json]
mlink status [--json]
mlink doctor [codex|pi|hermes] [--json]
mlink config diff [--json]
mlink backup list [--json]
mlink backup create
mlink backup restore <backup-id> [--dry-run]
mlink uninstall [codex] [pi] [hermes]
```

Interactive `install`, `restore`, and `uninstall` always print the exact plan and require `Apply? [y/N]`. The MemoryCore token is accepted through masked interactive input or `--memorycore-token-stdin`; it is never accepted as an argv value. JSON mode is non-mutating unless invoked with `--apply-plan <plan-id>`, `--yes`, and the required Secret input channel; stale plans still fail at the application layer.

Stable exit codes: `0` success/declined, `1` operational failure, `2` usage, `3` conflict/stale plan, `4` pending trust or user action.

- [ ] **Step 4: Run CLI and full tests**

Run: `rtk go test ./internal/cli ./cmd/mlink`

Expected: PASS.

Run: `rtk go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/cli cmd/mlink/main.go cmd/mlink/main_test.go
rtk git commit -m "feat: expose reversible mlink cli workflows"
```

---

### Task 12: Doctor and Structured Runtime Verification

**Files:**
- Create: `internal/doctor/check.go`
- Create: `internal/doctor/check_test.go`
- Create: `internal/doctor/codex.go`
- Create: `internal/doctor/pi.go`
- Create: `internal/doctor/hermes.go`
- Create: `internal/doctor/broker.go`
- Modify: `internal/app/service.go`
- Modify: `internal/app/detect.go`

**Interfaces:**
- Produces: `doctor.Report`, `doctor.Check`, and per-Agent `Run` functions; `app.Service.Doctor` and `app.Service.Status`.
- Consumes read-only targets, Broker health, Codex Hook fingerprints, Pi extension fingerprint, and Hermes runtime probes.

- [ ] **Step 1: Write failing distinct-state tests**

```go
func TestReportDistinguishesCodexTrustFromBrokerFailure(t *testing.T) {
	report := Report{Checks: []Check{
		{ID: "codex.hook", State: StatePendingAction, Code: "awaiting_trust"},
		{ID: "broker.socket", State: StateFailed, Code: "socket_unavailable"},
	}}
	if report.ExitCode() != 1 { t.Fatalf("exit = %d", report.ExitCode()) }
	if report.Checks[0].Code == report.Checks[1].Code { t.Fatal("states collapsed") }
}
```

- [ ] **Step 2: Run tests and observe failures**

Run: `rtk go test ./internal/doctor ./internal/app`

Expected: FAIL because structured checks do not exist.

- [ ] **Step 3: Implement read-only checks**

Check IDs and stable codes:

```text
broker.launchagent: active | inactive | plist_mismatch
broker.socket: reachable | socket_unavailable | permission_invalid
provider.tencentdb: available | auth_failed | backend_unavailable
codex.hook: active | awaiting_trust | config_conflict | missing
pi.extension: active | version_unsupported | fingerprint_mismatch | missing
hermes.provider: active | provider_mismatch | builtin_memory_active | identity_missing
hermes.bridge: reachable | dns_failed | auth_failed | timeout
journal.queue: clean | retrying | ambiguous | permanent_failure
```

Codex becomes `active` only after the Hook handler records a runtime heartbeat in Journal; file presence alone is `awaiting_trust`. Hermes runtime verification runs inside the selected OrbStack machine and confirms the Provider plus all three built-in memory shutdown settings.

- [ ] **Step 4: Run focused tests**

Run: `rtk go test ./internal/doctor ./internal/app ./internal/cli`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/doctor internal/app internal/cli
rtk git commit -m "feat: add structured mlink diagnostics"
```

---

### Task 13: Guided TUI and Dashboard

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/tui/model.go`
- Create: `internal/tui/update.go`
- Create: `internal/tui/view.go`
- Create: `internal/tui/theme.go`
- Create: `internal/tui/wizard.go`
- Create: `internal/tui/dashboard.go`
- Create: `internal/tui/model_test.go`
- Create: `internal/tui/view_test.go`
- Create: `internal/tui/testdata/wizard-120.golden`
- Create: `internal/tui/testdata/wizard-72.golden`
- Create: `internal/tui/testdata/dashboard-120.golden`
- Modify: `internal/cli/run.go`

**Interfaces:**
- Produces: `tui.New(app.Service) tea.Model` and the default `mlink` interactive entry.
- Consumes only `app.Service`, `install.ChangeSet`, and `doctor.Report`.

- [ ] **Step 1: Add Go 1.22-compatible TUI dependencies**

Run: `rtk go get github.com/charmbracelet/bubbletea@v1.1.2 github.com/charmbracelet/bubbles@v0.20.0 github.com/charmbracelet/lipgloss@v1.0.0`

Expected: all three selected modules declare Go 1.18 and do not raise the project `go` directive.

- [ ] **Step 2: Write failing navigation, alignment, narrow-mode, and no-write tests**

```go
func TestWizardCannotApplyBeforePreviewConfirmation(t *testing.T) {
	app := &fakeApp{plan: fixtureInstallPlan()}
	model := New(app)
	model = driveToAgentSelection(t, model)
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.applyCalls != 0 { t.Fatalf("apply calls = %d", app.applyCalls) }
	model = driveToPreviewAndConfirm(t, model)
	if app.applyCalls != 1 { t.Fatalf("apply calls = %d", app.applyCalls) }
}

func TestNarrowViewHasNoOverflow(t *testing.T) {
	view := renderAtWidth(t, 72)
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 72 { t.Fatalf("overflow %d: %q", lipgloss.Width(line), line) }
	}
}
```

- [ ] **Step 3: Run tests and observe missing TUI failures**

Run: `rtk go test ./internal/tui`

Expected: FAIL because the TUI model does not exist.

- [ ] **Step 4: Implement the nine-step wizard and daily dashboard**

Use structured rows, not padded strings. State transitions are explicit:

```go
type Step int
const (
	StepWelcome Step = iota
	StepDetect
	StepProvider
	StepConnection
	StepIdentity
	StepAgents
	StepPreview
	StepApply
	StepVerify
)
```

The Provider list shows only actually installed TencentDB. The Agent list is a multi-select. Preview displays file path, semantic old/new value, protected configuration status, and warnings. `Enter` on Preview opens confirmation; only the affirmative action calls `ApplyInstall` with the displayed plan ID.

The Connection screen collects `base_url`, `service_id`, `team_id`, logical memory `agent_id`, local fixed `user_id`, shared L2/L3 opt-in, and a masked MemoryCore token. The token remains only in the TUI model until Apply, is passed again through `InstallRequest.SecretInputs`, and is overwritten in memory after Apply returns. Preview shows only its Keychain reference and `configured/not configured` state.

Use the confirmed gold/navy visual system and filled MLink pixel wordmark. Respect `NO_COLOR`; at widths below 80, hide secondary columns and use a single-column text layout. Status dots appear at the end of their grid row, not before the label.

- [ ] **Step 5: Run focused, baseline, and Go 1.22 tests**

Run: `rtk go test ./internal/tui ./internal/cli`

Expected: PASS and all goldens match.

Run: `rtk go test ./...`

Expected: PASS.

Run: `rtk GOTOOLCHAIN=go1.22.12 go test ./...`

Expected: PASS with no toolchain auto-upgrade.

- [ ] **Step 6: Commit**

```bash
rtk git add go.mod go.sum internal/tui internal/cli
rtk git commit -m "feat: add guided mlink terminal interface"
```

---

### Task 14: Isolated Installation, Restore, Uninstall, and Adversarial Suite

**Files:**
- Create: `internal/e2e/install_test.go`
- Create: `internal/e2e/lifecycle_test.go`
- Create: `internal/e2e/adversarial_test.go`
- Create: `internal/e2e/testdata/codex/hooks.json`
- Create: `internal/e2e/testdata/hermes/config.yaml`
- Create: `internal/e2e/testdata/pi/settings.json`
- Create: `docs/testing/mlink-user-layer-isolated.md`

**Interfaces:**
- Validates all public interfaces from Tasks 1–13 without touching real user state.

- [ ] **Step 1: Write a failing temporary-HOME installation round trip**

```go
func TestInstallRestoreUninstallRoundTrip(t *testing.T) {
	fixture := NewFixture(t)
	before := fixture.SnapshotAll(t)
	plan := fixture.PlanInstall(t, "codex", "pi", "hermes")
	fixture.AssertNoWrites(t)
	fixture.Apply(t, plan)
	fixture.AssertInstalledAndProtected(t)
	fixture.Uninstall(t)
	after := fixture.SnapshotAll(t)
	if !reflect.DeepEqual(before, after) { t.Fatalf("after = %#v, want %#v", after, before) }
}
```

- [ ] **Step 2: Add failing adversarial cases**

Create table-driven cases for:

```text
same event replayed 10 times
same turn_id with changed content
assistant fragment before user fragment
missing Codex Stop
repeated Pi agent_end before one agent_settled
missing and repeated Pi agent_settled
two delegated Feishu users interleaved 100 times
missing stable identity
nickname changes and identical nicknames with different stable IDs
Provider timeout before send
Provider response lost after send
Provider malformed frame and crash
Broker restart with queued events
stale install plan
user edits unrelated config before uninstall
user edits owned config before uninstall
memory text containing a fake system instruction
```

- [ ] **Step 3: Run the suite and observe failures**

Run: `rtk go test ./internal/e2e -count=1`

Expected: FAIL until the fixture exposes every missing edge or the implementation is corrected.

- [ ] **Step 4: Make only the implementation fixes required by failing cases**

For each failure, change the owning package rather than adding exceptions in the E2E fixture. Record the final behavior in `docs/testing/mlink-user-layer-isolated.md`, explicitly separating event-level duplicate prevention from TencentDB semantic behavior.

- [ ] **Step 5: Run the full verification matrix**

Run: `rtk go test ./...`

Expected: PASS.

Run: `rtk go test -race ./...`

Expected: PASS.

Run: `rtk go test -shuffle=on -count=2 ./...`

Expected: PASS twice in shuffled order.

Run: `rtk go vet ./...`

Expected: PASS.

Run: `rtk GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 go test ./...`

Expected: PASS; SQLite remains pure Go.

- [ ] **Step 6: Commit**

```bash
rtk git add internal/e2e docs/testing/mlink-user-layer-isolated.md internal/adapter internal/app internal/broker internal/install internal/journal internal/tui internal/doctor
rtk git commit -m "test: verify mlink user-layer lifecycle"
```

---

### Task 15: Read-Only Real-Machine ChangeSet

**Files:**
- Create: `docs/testing/mlink-user-layer-real-preview.md`

**Interfaces:**
- Consumes the built `mlink` binary and current-machine read-only detection.
- Produces no Agent, LaunchAgent, Keychain, OrbStack, or MemoryCore mutation.

- [ ] **Step 1: Build a fixed candidate binary without installing it**

Run: `rtk go build -trimpath -o /tmp/mlink-user-layer-preview ./cmd/mlink`

Expected: build succeeds; `~/.local/bin/mlink` is unchanged.

- [ ] **Step 2: Capture hashes of protected real configuration before preview**

Run read-only commands that hash `~/.codex/config.toml`, Pi settings, the selected Hermes config, and the current installed MLink binary when present. Do not print file contents or Secret values.

- [ ] **Step 3: Generate the real dry-run ChangeSet**

Run: `rtk /tmp/mlink-user-layer-preview install --dry-run codex pi hermes --json --memorycore-token-stdin`, supplying the existing token through stdin without printing it.

Expected: exit `0`; output includes actual paths, current Hermes Provider to `mlink`, both built-in memory flags to `false`, the owned `memory` disabled-toolset entry, LaunchAgent creation, and all protected model/auth invariants as unchanged.

- [ ] **Step 4: Prove preview made no writes**

Repeat the Step 2 hashes and compare them byte-for-byte. Confirm no new LaunchAgent, Hook, Extension, Hermes plugin, Keychain item, state database, or Socket was created.

- [ ] **Step 5: Record and commit the redacted preview report**

Write `docs/testing/mlink-user-layer-real-preview.md` with versions, paths, semantic changes, invariant results, and warnings. Do not include Secret values, raw Feishu IDs, or full model credentials.

```bash
rtk git add docs/testing/mlink-user-layer-real-preview.md
rtk git commit -m "docs: record mlink real installation preview"
```

- [ ] **Step 6: Stop for explicit user confirmation**

Show the real ChangeSet to the user. Do not run Apply until the user explicitly confirms that exact preview.

---

### Task 16: Apply to Current Machine and Run Live Acceptance

**Files:**
- Create: `docs/testing/mlink-user-layer-live.md`

**Interfaces:**
- Starts only after Task 15's exact ChangeSet is explicitly approved.

- [ ] **Step 1: Apply the approved plan ID**

Run the candidate with `install --apply-plan <approved-plan-id> --yes --memorycore-token-stdin`, supplying the token only through stdin. The application layer must reject any stale before-hash and regenerate a preview instead of guessing.

- [ ] **Step 2: Verify Broker, Provider, and configuration protection**

Run `mlink doctor --json`, re-hash protected Agent model/auth configuration, and verify the hashes or semantic protected snapshots match Task 15.

- [ ] **Step 3: Complete Codex Hook trust**

Open Codex `/hooks`, let the user review and trust the installed MLink definitions, then run `mlink doctor codex --json` until the runtime heartbeat changes from `awaiting_trust` to `active`. Do not use `--dangerously-bypass-hook-trust`.

- [ ] **Step 4: Verify Pi and Hermes real lifecycle**

Run one dedicated Pi test turn and one Hermes dedicated test turn. For Hermes, verify from `hermes-agent-env` that `memory.provider=mlink`, built-in memory injection and user profile are disabled, the `memory` toolset is absent, and `prefetch/sync_turn` reach the host Broker without blocking the reply.

- [ ] **Step 5: Run live multi-user and replay tests in a dedicated test scope**

After separately displaying the exact test identity and write scope, run two stable Feishu users with interleaved canaries, replay one completed event ten times, and verify the Journal sends one logical capture. Do not write test content into the user's normal memory identity.

- [ ] **Step 6: Record and commit the acceptance report**

Write `docs/testing/mlink-user-layer-live.md` with command shapes, elapsed times, non-secret IDs/hashes, pass/fail state, and the distinction between MLink results and TencentDB semantic results.

```bash
rtk git add docs/testing/mlink-user-layer-live.md
rtk git commit -m "docs: record mlink user-layer live acceptance"
```

---

## Final Self-Review Checklist

- Every requirement in sections 1–16 of the user-layer spec maps to at least one task above.
- Codex uses official Hooks and never parses transcripts.
- Pi commits only after `agent_settled`; duplicate `agent_end` is tested.
- Hermes uses official Provider and config surfaces; no Hermes source patch exists in the plan.
- Hermes built-in files are retained and restorable.
- SQLite implements event idempotency and delivery state, not semantic memory logic.
- Replay-unsafe maybe-sent writes stop at `ambiguous`.
- Preview and Apply share the exact ChangeSet and before-hash contract.
- Config backups and ownership are sufficient for restore and uninstall without whole-file clobbering.
- Agent model/auth fields are unchanged and tested.
- The real-machine preview is read-only and precedes a separate Apply confirmation.
- The user's existing `README.md` modification is not staged by any plan task.
