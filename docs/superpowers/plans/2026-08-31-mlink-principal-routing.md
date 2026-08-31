# MLink Stable Principal and Hermes Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace direct external-ID-derived memory identities with stable MLink Principals, reversible alias bindings, and explicit personal/private/group Memory Spaces so Codex, Pi, Hermes DMs, groups, and topics receive the intended L1/L2/L3 scope.

**Architecture:** Keep one TencentDB Provider Connection and split identity policy into three logical Memory Spaces. A Broker-side identity router resolves fixed local Agents, bound Feishu DMs, unbound Feishu DMs, and Feishu groups into canonical Provider identities before raw external fields can reach the Journal or Provider. Owner aliases live in Keychain, identity configuration is exportable as an encrypted bundle, and the existing ChangeSet transaction remains the only mutation path.

**Tech Stack:** Go 1.22, standard `crypto/aes`, `crypto/cipher`, `crypto/hmac`, `crypto/rand`, `crypto/sha256`, `encoding/json`, `net/http`; `golang.org/x/crypto/scrypt`; `gopkg.in/yaml.v3 v3.0.1`; `modernc.org/sqlite v1.34.5`; existing Bubble Tea/Bubbles/Lip Gloss versions; macOS Keychain; OrbStack direct argv; TencentDB MemoryCore v2.0.1.

**Spec:** `docs/superpowers/specs/2026-08-31-mlink-principal-routing-design.md`

## Global Constraints

- Work only on the current production-base feature branch; never implement directly on `main`, `master`, `develop`, `development`, `release`, `test`, or `testing`.
- Preserve the user's unstaged `README.md` change and stage implementation paths explicitly; never use `git add -A` or `git add -a`.
- Every shell command in this repository starts with `rtk`.
- Run Go commands as `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go ...`; use `GOTOOLCHAIN=go1.22.12` for the compatibility gate.
- Use `apply_patch` for source and document edits.
- Follow strict TDD: add one failing behavior test, observe the expected failure, add the minimum implementation, rerun focused tests, then commit.
- Do not proxy or change Agent LLM traffic, model provider, Base URL, API key, subscription, login, or authentication flow.
- Do not patch Hermes or TencentDB source. Modify Hermes only through official Provider and configuration surfaces.
- Raw Feishu user, group, and topic identifiers may exist only in the authenticated Hermes→Broker request, masked TUI memory, Keychain, or encrypted Identity Bundle. They must not enter non-secret config, ChangeSet JSON, Journal rows, logs, Provider requests, test failure output, or commits.
- Preview remains zero-write. Principal IDs, canonical user IDs, and Binding slot IDs in a ChangeSet depend only on non-secret inputs and must be reproducible in a separate Apply process.
- Existing `plan_5b496cbe0ed3a1c09525934f9c` is permanently invalid and must never be applied.
- Never touch real Codex, Pi, Hermes, LaunchAgent, Keychain, or MemoryCore state in Tasks 1–12.
- Task 13 writes only to unique isolated MemoryCore identities after displaying the exact scope. Task 14 is a read-only real-machine preview and stops for fresh user confirmation.
- `go test -race` is still attempted, but the known local Xcode `arm64`/`arm64e` `libxcrun.dylib` failure must be reported as an environment build blocker rather than hidden or treated as a project test failure.

---

## File and Package Map

### Configuration and identity

- Modify `internal/config/types.go`: schema v2 `Connection`, `Principal`, `MemorySpace`, `BindingRef`, typed `Adapter` routing.
- Modify `internal/config/store.go`: strict v2 validation and explicit v1 migration preview.
- Create `internal/identity/types.go`: external context, resolved identity, stable IDs.
- Create `internal/identity/router.go`: fixed, owner-binding, private-DM, and group routing.
- Create `internal/identity/bindings.go`: constant-time owner Binding matching and redacted descriptors.
- Create `internal/identity/bundle.go`: encrypted portable identity bundle.
- Create `internal/identity/candidates.go`: neutral detected-candidate model used by Hermes and TUI.

### Broker, model, and Journal

- Modify `internal/broker/auth.go`: fixed and delegated grants reference allowed Spaces instead of embedding one identity.
- Modify `internal/broker/http.go`: accept complete Hermes external context and drop it after authorization.
- Modify `internal/model/model.go`: add local `ActorDigest` to canonical Turn/Fragment metadata without Provider serialization.
- Create `internal/journal/migrations/003_actor_digest.sql`: persist only pseudonymous actor digests.
- Modify `internal/journal/events.go`: include actor digest in group event idempotency and conflict boundaries.

### Hermes and installation

- Modify `internal/adapter/hermes/templates/__init__.py.tmpl`: send chat/user context and include actor identity in turn-ID hashing.
- Modify `internal/adapter/hermes/config.go`: own and restore both Session-sharing booleans.
- Create `internal/adapter/hermes/candidates.go`: read bounded candidate metadata from the active Orb Session DB.
- Modify `internal/app/service.go`, `install.go`, `restore.go`, `uninstall.go`: schema v2 desired state and Keychain Binding transaction.
- Modify `cmd/mlink/runtime.go`, `diagnostics.go`: load v2 Spaces/Bindings and construct the dynamic Router.

### User workflows and tests

- Create `internal/cli/identity.go`: list/bind/rebind/revoke/export/import.
- Modify `internal/cli/run.go`, `run_test.go`: route identity commands with stable exit codes.
- Modify `internal/tui/model.go`, `view.go`: owner candidate selection and three-Space preview.
- Extend `internal/e2e`: offline identity, group, alias-rotation, rollback, export/import, and secret-leak suites.
- Create `docs/testing/mlink-principal-routing-isolated.md` and `docs/testing/mlink-principal-routing-live.md`.

---

### Task 1: Configuration Schema v2 and Explicit v1 Boundary

**Files:**
- Modify: `internal/config/types.go`
- Modify: `internal/config/store.go`
- Modify: `internal/config/store_test.go`
- Create: `internal/config/migrate.go`
- Create: `internal/config/migrate_test.go`

**Interfaces:**
- Produces: `config.Principal`, `config.MemorySpace`, `config.BindingRef`, `config.HermesRouting`, `config.ErrMigrationRequired`, `config.PreviewV1Migration([]byte) (MigrationPreview, error)`.
- Consumes: existing atomic YAML Store and Connection Provider fields.

- [ ] **Step 1: Write failing schema-v2 round-trip and validation tests**

```go
func TestStoreRoundTripsV2SpacesWithoutLegacyIdentity(t *testing.T) {
    want := Config{
        SchemaVersion: 2,
        NamespaceID: "personal",
        ActiveConnectionID: "local",
        Connections: map[string]Connection{"local": {
            ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0",
            ConfigRevision: "rev-2", ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
            SecretRefs: map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
        }},
        Principals: map[string]Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: PrincipalPerson}},
        Spaces: map[string]MemorySpace{
            "personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: PolicyFixed, PrincipalID: "owner"},
            "hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: PolicyExternalHMAC},
            "hermes-groups": {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: PolicyGroupHMAC},
        },
        Adapters: map[string]Adapter{
            "codex": {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
            "hermes": {ID: "hermes", Enabled: true, HermesRouting: &HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}},
        },
        Bindings: map[string]BindingRef{"owner-feishu-union-1": {ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: BindingActive}},
    }
    store := Store{Path: filepath.Join(t.TempDir(), "config.yaml")}
    if err := store.SaveAtomic(want); err != nil { t.Fatal(err) }
    got, err := store.Load()
    if err != nil || !reflect.DeepEqual(got, want) { t.Fatalf("round trip = %#v, %v", got, err) }
}

func TestV2RejectsBindingSlotThatLeaksExternalValue(t *testing.T) {
    cfg := fixtureV2Config()
    cfg.Bindings["owner-feishu-union-1"] = BindingRef{ID: "on_actual_union_id", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: BindingActive}
    if err := Validate(cfg); err == nil { t.Fatal("Validate() error = nil") }
}
```

- [ ] **Step 2: Run the focused tests and observe missing v2 types**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/config -run 'TestStoreRoundTripsV2|TestV2Rejects' -count=1`

Expected: FAIL because `Principal`, `MemorySpace`, `HermesRouting`, and `Validate` v2 rules do not exist.

- [ ] **Step 3: Implement the v2 types and strict graph validation**

```go
type PrincipalKind string
type PrincipalPolicy string
type BindingStatus string

const (
    PrincipalPerson PrincipalKind = "person"
    PolicyFixed PrincipalPolicy = "fixed"
    PolicyExternalHMAC PrincipalPolicy = "external_hmac"
    PolicyGroupHMAC PrincipalPolicy = "group_hmac"
    BindingActive BindingStatus = "active"
    BindingRevoked BindingStatus = "revoked"
)

type Principal struct { ID, CanonicalUserID string; Kind PrincipalKind }
type MemorySpace struct {
    ID, ConnectionID, TenantID, AgentID string
    IncludeAgentShared bool
    PrincipalPolicy PrincipalPolicy
    PrincipalID string
}
type BindingRef struct { ID, Source, Kind, PrincipalID, SecretRef string; Status BindingStatus }
type HermesRouting struct { OwnerSpaceID, PrivateSpaceID, GroupSpaceID string }
type Adapter struct { ID string; Enabled bool; SpaceID string; HermesRouting *HermesRouting }
```

`Validate` must reject dangling Connection/Space/Principal references, duplicate Secret refs, unsafe IDs outside `[A-Za-z0-9._-]{1,128}`, fixed Spaces without a Principal, dynamic Spaces with a fixed Principal, `include_agent_shared=true` outside `personal-owner`, and schema-v2 Connections containing legacy tenant/Agent/user/shared fields.

- [ ] **Step 4: Write and verify the explicit v1 migration-preview test**

```go
func TestV1RequiresExplicitMigrationPreview(t *testing.T) {
    raw := []byte("schema_version: 1\nactive_connection_id: local\nconnections:\n  local:\n    id: local\n    provider_id: dev.mlink.tencentdb\n    provider_version: 0.1.0\n    config_revision: rev-1\n    tenant_id: personal\n    agent_id: default\n    user_id: keliang\n")
    _, err := Decode(raw)
    if !errors.Is(err, ErrMigrationRequired) { t.Fatalf("Decode() error = %v", err) }
    preview, err := PreviewV1Migration(raw)
    if err != nil { t.Fatal(err) }
    if preview.FromVersion != 1 || preview.ToVersion != 2 || preview.OwnerCanonicalUserID != "usr_owner_keliang" { t.Fatalf("preview = %#v", preview) }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/config -count=1`

Expected: PASS; loading v1 never silently reinterprets identity.

- [ ] **Step 5: Commit schema v2**

```bash
rtk git add internal/config/types.go internal/config/store.go internal/config/store_test.go internal/config/migrate.go internal/config/migrate_test.go
rtk git commit -m "feat: define mlink principal configuration v2"
```

---

### Task 2: Stable Principals, Bindings, and Space Router

**Files:**
- Create: `internal/identity/types.go`
- Create: `internal/identity/router.go`
- Create: `internal/identity/router_test.go`
- Create: `internal/identity/bindings.go`
- Create: `internal/identity/bindings_test.go`
- Modify: `internal/identity/resolver.go`
- Modify: `internal/identity/resolver_test.go`

**Interfaces:**
- Consumes: config v2 Principal, MemorySpace, BindingRef.
- Produces: `identity.ExternalContext`, `identity.ResolvedIdentity`, `identity.Router.ResolveFixed`, `identity.Router.ResolveHermes`, `identity.CanonicalTurnID`, `identity.BindingSet`.

- [ ] **Step 1: Write failing owner-alias and group-routing tests**

```go
func TestOldAndNewAliasesResolveToSameOwner(t *testing.T) {
    router := fixtureRouter(t, []BindingValue{
        {RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_old"), PrincipalID: "owner"},
        {RefID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", Value: []byte("on_new"), PrincipalID: "owner"},
    })
    old, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_old"})
    if err != nil { t.Fatal(err) }
    newer, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_new"})
    if err != nil { t.Fatal(err) }
    if old.UserID != "usr_owner_keliang" || newer.UserID != old.UserID || old.SpaceID != "personal-owner" { t.Fatalf("resolved = %#v %#v", old, newer) }
}

func TestGroupMembersSharePrincipalAndTopicSession(t *testing.T) {
    router := fixtureRouter(t, nil)
    a, _ := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_topic", AlternateSubject: "on_a"})
    b, _ := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_topic", AlternateSubject: "on_b"})
    other, _ := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_other", AlternateSubject: "on_b"})
    if a.UserID != b.UserID || a.SessionID != b.SessionID { t.Fatalf("same topic split: %#v %#v", a, b) }
    if a.UserID != other.UserID || a.SessionID == other.SessionID { t.Fatalf("topic scope = %#v %#v", a, other) }
    if a.IncludeAgentShared { t.Fatal("group enabled L2/L3") }
}
```

- [ ] **Step 2: Run the tests and observe missing Router APIs**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity -run 'TestOldAndNew|TestGroupMembers' -count=1`

Expected: FAIL because the stable Router and Binding model do not exist.

- [ ] **Step 3: Implement the Router with exact result types**

```go
type ExternalContext struct {
    Source, ChatType, ChatID, ThreadID string
    PrimarySubject, AlternateSubject string
}
type ResolvedIdentity struct {
    ConnectionID, SpaceID, TenantID, AgentID, UserID, SessionID string
    IncludeAgentShared bool
    ActorDigest string
}
type Router struct {
    NamespaceID string
    Key []byte
    Connections map[string]config.Connection
    Spaces map[string]config.MemorySpace
    Principals map[string]config.Principal
    Bindings BindingSet
    Hermes config.HermesRouting
}
```

Use domain-separated HMAC inputs:

```go
userID := "usr_" + hmacBase32(key, "external-user\x00"+namespace+"\x00"+source+"\x00"+subject)
groupID := "grp_" + hmacBase32(key, "group\x00"+namespace+"\x00"+source+"\x00"+chatID)
sessionID := "ses_" + hmacBase32(key, "session\x00"+source+"\x00"+chatID+"\x00"+threadID)
actorDigest := "actor_" + hmacBase32(key, "actor\x00"+source+"\x00"+subject)
turnID := "turn_" + hmacBase32(key, "turn\x00"+actorDigest+"\x00"+incomingTurnID)
```

Return `ErrIdentityMissing` for missing DM subject, `ErrGroupIdentityMissing` for missing group `chat_id`, and `ErrUnsupportedContext` for non-Feishu or unknown `chat_type`. Never use names or incoming Hermes `session_id` as identity input. `CanonicalTurnID` is applied only to delegated Hermes turns before Journal submission; Codex/Pi keep their official turn IDs.

- [ ] **Step 4: Add revoked, collision, fixed-Agent, and no-leak tests**

```go
func TestRevokedAliasAndUnknownAliasCannotResolveOwner(t *testing.T) {
    router := fixtureRouter(t, []BindingValue{{RefID: "old", Source: "feishu", Kind: "union_id", Value: []byte("on_old"), PrincipalID: "owner", Revoked: true}})
    _, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_old"})
    if !errors.Is(err, ErrBindingRevoked) { t.Fatalf("revoked alias error = %v", err) }
    got, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_never_bound"})
    if err != nil || got.SpaceID != "hermes-private" { t.Fatalf("unknown alias = %#v %v", got, err) }
}

func TestFixedCodexAndPiNeverNeedFeishuIdentity(t *testing.T) {
    router := fixtureRouter(t, nil)
    for _, adapter := range []string{"codex", "pi"} {
        got, err := router.ResolveFixed(adapter, "native-session")
        if err != nil || got.UserID != "usr_owner_keliang" || got.SessionID != "native-session" { t.Fatalf("%s = %#v %v", adapter, got, err) }
    }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity -count=1`

Expected: PASS; test errors and `%#v` output must redact `BindingValue.Value` through a custom `String`/`GoString` implementation.

- [ ] **Step 5: Commit the identity router**

```bash
rtk git add internal/identity/types.go internal/identity/router.go internal/identity/router_test.go internal/identity/bindings.go internal/identity/bindings_test.go internal/identity/resolver.go internal/identity/resolver_test.go
rtk git commit -m "feat: route stable principals across memory spaces"
```

---

### Task 3: Keychain Binding Repository and Transactional Alias Changes

**Files:**
- Create: `internal/identity/store.go`
- Create: `internal/identity/store_test.go`
- Modify: `internal/secret/store.go`
- Modify: `internal/secret/keychain_test.go`

**Interfaces:**
- Consumes: `secret.Store`, config BindingRef.
- Produces: `identity.Repository.Load`, `PlanBind`, `ApplyBind`, `PlanRevoke`, `ApplyRevoke` with rollback-safe Keychain mutations.

- [ ] **Step 1: Write failing secret-independence and rollback tests**

```go
func TestPlanBindDoesNotPersistOrExposeExternalID(t *testing.T) {
    secrets := newMemorySecretStore()
    repository := Repository{Secrets: secrets, IdentityKey: bytes.Repeat([]byte{0x2a}, 32)}
    plan, err := repository.PlanBind(BindRequest{SlotID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", Value: []byte("on_actual")})
    if err != nil { t.Fatal(err) }
    raw, _ := json.Marshal(plan)
    if secrets.Puts != 0 || bytes.Contains(raw, []byte("on_actual")) { t.Fatalf("unsafe plan: puts=%d json=%s", secrets.Puts, raw) }
}

func TestApplyBindFailureRestoresPreviousKeychainValue(t *testing.T) {
    secrets := newMemorySecretStore()
    secrets.Values["identity/binding/owner-feishu-union-1"] = []byte("on_old")
    secrets.FailPutAt = 2
    repository := Repository{Secrets: secrets, IdentityKey: bytes.Repeat([]byte{0x2a}, 32)}
    err := repository.ApplyChanges(context.Background(), []Change{{Account: "identity/binding/owner-feishu-union-1", Value: []byte("on_new")}, {Account: "identity/binding/owner-feishu-union-2", Value: []byte("on_second")}})
    if err == nil || string(secrets.Values["identity/binding/owner-feishu-union-1"]) != "on_old" { t.Fatalf("rollback = %q, %v", secrets.Values["identity/binding/owner-feishu-union-1"], err) }
}
```

- [ ] **Step 2: Run focused tests and observe missing Repository**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity -run 'TestPlanBind|TestApplyBindFailure' -count=1`

Expected: FAIL because Repository and alias Change types do not exist.

- [ ] **Step 3: Implement stable slot accounts and constant-time loading**

```go
type Repository struct { Secrets secret.Store; IdentityKey []byte }
type Change struct { Account string; Value []byte; Delete bool }

func BindingAccount(slotID string) (string, error) {
    if !safeIDPattern.MatchString(slotID) { return "", errors.New("invalid binding slot") }
    return "identity/binding/" + slotID, nil
}
```

`Load` reads each active BindingRef SecretRef, validates one-line non-empty values, computes an in-memory HMAC fingerprint, and returns BindingValues. It must wipe temporary bytes. `ApplyChanges` snapshots every prior value, applies in order, and restores in reverse order on any failure.

- [ ] **Step 4: Run Keychain and identity tests**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/secret ./internal/identity -count=1`

Expected: PASS; fake Runner confirms raw IDs appear only on Keychain stdin and never command argv.

- [ ] **Step 5: Commit the Binding repository**

```bash
rtk git add internal/identity/store.go internal/identity/store_test.go internal/secret/store.go internal/secret/keychain_test.go
rtk git commit -m "feat: persist reversible identity bindings in keychain"
```

---

### Task 4: Broker External Context and Dynamic Space Authorization

**Files:**
- Modify: `internal/broker/auth.go`
- Modify: `internal/broker/http.go`
- Modify: `internal/broker/http_test.go`
- Modify: `internal/broker/server_test.go`
- Modify: `internal/adapter/client/client.go`
- Modify: `internal/adapter/client/client_test.go`

**Interfaces:**
- Consumes: identity Router and ResolvedIdentity.
- Produces: `broker.ExternalContextInput`, dynamic delegated grants, canonical-only Service requests.

- [ ] **Step 1: Write failing HTTP tests for owner DM, other DM, group, and raw-ID dropping**

```go
func TestHermesHTTPRoutesOwnerOtherAndGroupWithoutForwardingRawIDs(t *testing.T) {
    server, provider := testRoutedHermesServer(t)
    cases := []struct{ name string; input map[string]any; wantAgentID, wantUserPrefix string; wantShared bool }{
        {"owner", map[string]any{"adapter_id":"hermes","source":"feishu","chat_type":"dm","chat_id":"oc_dm","alternate_subject":"on_owner","query":"x"}, "keliang-personal", "usr_owner_", true},
        {"other", map[string]any{"adapter_id":"hermes","source":"feishu","chat_type":"dm","chat_id":"oc_dm2","alternate_subject":"on_other","query":"x"}, "hermes-private", "usr_", false},
        {"group", map[string]any{"adapter_id":"hermes","source":"feishu","chat_type":"group","chat_id":"oc_group","thread_id":"omt_topic","alternate_subject":"on_other","query":"x"}, "hermes-groups", "grp_", false},
    }
    for _, tc := range cases {
        response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", tc.input)
        response.Body.Close()
        if response.StatusCode != http.StatusOK { t.Fatalf("%s status=%d", tc.name, response.StatusCode) }
        got := provider.LastRequest()
        if got.Identity.AgentID != tc.wantAgentID || !strings.HasPrefix(got.Identity.UserID, tc.wantUserPrefix) || got.IncludeAgentShared != tc.wantShared { t.Fatalf("%s got=%#v", tc.name, got) }
        if strings.Contains(got.Identity.UserID, "on_") { t.Fatal("raw external ID reached Provider") }
    }
}
```

- [ ] **Step 2: Run the Broker tests and observe rejected unknown fields**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/broker -run TestHermesHTTPRoutes -count=1`

Expected: FAIL because current strict decoder does not accept `chat_type/chat_id/thread_id/alternate_subject` and Grant has one fixed identity.

- [ ] **Step 3: Implement the delegated authorization boundary**

```go
type ExternalContextInput struct {
    Source string `json:"source,omitempty"`
    ChatType string `json:"chat_type,omitempty"`
    ChatID string `json:"chat_id,omitempty"`
    ThreadID string `json:"thread_id,omitempty"`
    PrimarySubject string `json:"primary_subject,omitempty"`
    AlternateSubject string `json:"alternate_subject,omitempty"`
}
type Grant struct {
    TokenDigest []byte
    AdapterID string
    Mode IdentityMode
    Source string
    FixedSpaceID string
    AllowedSpaceIDs map[string]bool
}
```

`Authorizer.Resolve` authenticates the token/Adapter first, calls `Router.ResolveFixed` or `Router.ResolveHermes`, verifies the resolved Space is allowed by the Grant, then constructs the canonical route/identity. HTTP handlers must not pass `ExternalContextInput` beyond authorization.

- [ ] **Step 4: Add fail-closed, body-limit, and response-redaction tests**

```go
func TestHermesGroupMissingChatIDFailsWithoutProviderCall(t *testing.T) {
    server, provider := testRoutedHermesServer(t)
    response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", map[string]any{"adapter_id":"hermes","source":"feishu","chat_type":"group","alternate_subject":"on_a","query":"x"})
    defer response.Body.Close()
    if response.StatusCode != http.StatusForbidden || provider.Calls() != 0 { t.Fatalf("status/calls=%d/%d", response.StatusCode, provider.Calls()) }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/broker ./internal/adapter/client -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Broker routing**

```bash
rtk git add internal/broker/auth.go internal/broker/http.go internal/broker/http_test.go internal/broker/server_test.go internal/adapter/client/client.go internal/adapter/client/client_test.go
rtk git commit -m "feat: authorize hermes memory spaces dynamically"
```

---

### Task 5: Journal Actor Digest and Group Idempotency

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/model/model_test.go`
- Create: `internal/journal/migrations/003_actor_digest.sql`
- Modify: `internal/journal/store.go`
- Modify: `internal/journal/events.go`
- Modify: `internal/journal/events_test.go`

**Interfaces:**
- Consumes: identity ResolvedIdentity.ActorDigest.
- Produces: canonical group event conflict/deduplication that never persists raw actor identity.

- [ ] **Step 1: Write failing group actor collision and schema-leak tests**

```go
func TestGroupActorsWithSameTurnIDDoNotCollide(t *testing.T) {
    store := openTestStore(t)
    first := fixtureEnvelope("grp_same", "turn_same", "hello")
    first.Turn.Identity.SessionID = "ses_topic"
    first.Turn.ActorDigest = "actor_a"
    first.Turn.Identity.TurnID = identity.CanonicalTurnID(bytes.Repeat([]byte{0x2a}, 32), "actor_a", "turn_same")
    second := fixtureEnvelope("grp_same", "turn_same", "hello")
    second.Turn.Identity.SessionID = "ses_topic"
    second.Turn.ActorDigest = "actor_b"
    second.Turn.Identity.TurnID = identity.CanonicalTurnID(bytes.Repeat([]byte{0x2a}, 32), "actor_b", "turn_same")
    a, insertedA, err := store.EnqueueTurn(context.Background(), first)
    if err != nil || !insertedA { t.Fatalf("first=%#v %v", a, err) }
    b, insertedB, err := store.EnqueueTurn(context.Background(), second)
    if err != nil || !insertedB || a.ID == b.ID { t.Fatalf("second=%#v %v", b, err) }
}

func TestJournalNeverPersistsRawExternalIdentity(t *testing.T) {
    store := openTestStore(t)
    envelope := fixtureEnvelope("grp_same", "turn", "content")
    envelope.Turn.ActorDigest = "actor_safe"
    _, _, _ = store.EnqueueTurn(context.Background(), envelope)
    var count int
    _ = store.db.QueryRow(`SELECT count(*) FROM journal_events WHERE CAST(payload AS TEXT) LIKE '%on_actual%' OR actor_digest LIKE '%on_actual%'`).Scan(&count)
    if count != 0 { t.Fatalf("raw identity rows=%d", count) }
}
```

- [ ] **Step 2: Run focused tests and observe missing ActorDigest**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/model ./internal/journal -run 'TestGroupActors|TestJournalNever' -count=1`

Expected: FAIL because model and migration schema lack actor digest.

- [ ] **Step 3: Add the migration and idempotency input**

```sql
ALTER TABLE turn_fragments ADD COLUMN actor_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE journal_events ADD COLUMN actor_digest TEXT NOT NULL DEFAULT '';
```

Include `ActorDigest` in event idempotency material. Set `ActorDigest` to `json:"-"` so it is not serialized into the Provider `Turn`; persist and scan it separately from the payload. Existing Journal uniqueness remains valid because delegated Hermes incoming turn IDs are canonicalized with actor digest before fragments/events are recorded.

- [ ] **Step 4: Run Journal and Broker tests**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/model ./internal/journal ./internal/broker -count=1`

Expected: PASS, including migration from an existing v2 Journal.

- [ ] **Step 5: Commit actor-safe journaling**

```bash
rtk git add internal/model/model.go internal/model/model_test.go internal/journal/migrations/003_actor_digest.sql internal/journal/store.go internal/journal/events.go internal/journal/events_test.go
rtk git commit -m "feat: isolate group actors in the delivery journal"
```

---

### Task 6: Hermes Context Bridge, Shared Session Config, and Candidate Detection

**Files:**
- Modify: `internal/adapter/hermes/templates/__init__.py.tmpl`
- Modify: `internal/adapter/hermes/plan_test.go`
- Modify: `internal/adapter/hermes/config.go`
- Modify: `internal/adapter/hermes/config_test.go`
- Create: `internal/adapter/hermes/candidates.go`
- Create: `internal/adapter/hermes/candidates_test.go`

**Interfaces:**
- Consumes: Broker `ExternalContextInput`, OrbTarget/CommandRunner.
- Produces: complete official MemoryProvider context payload; `hermes.DetectIdentityCandidates`.

- [ ] **Step 1: Write failing template and config-ownership tests**

```go
func TestProviderSendsCompleteContextWithoutDirectCanonicalUser(t *testing.T) {
    resources, err := PlanProvider("/home/test/.hermes", HTTPGrant{Endpoint: "http://192.168.139.3:8097", Token: "secret"})
    if err != nil { t.Fatal(err) }
    module := string(resources[1].Content)
    for _, want := range []string{`"chat_type": self._chat_type`, `"chat_id": self._chat_id`, `"thread_id": self._thread_id`, `"primary_subject": self._primary_subject`, `"alternate_subject": self._alternate_subject`} {
        if !strings.Contains(module, want) { t.Fatalf("template missing %s", want) }
    }
    if strings.Contains(module, `"user_id":`) { t.Fatal("template can override canonical user") }
}

func TestMergeConfigMakesGroupsAndThreadsSharedAndRestoresExactly(t *testing.T) {
    before := []byte("group_sessions_per_user: true\nmemory:\n  provider: hy-memory\n")
    after, ownership, err := MergeConfig(before)
    if err != nil { t.Fatal(err) }
    if !bytes.Contains(after, []byte("group_sessions_per_user: false")) || !bytes.Contains(after, []byte("thread_sessions_per_user: false")) { t.Fatalf("after=%s", after) }
    restored, err := RestoreConfig(after, ownership)
    if err != nil || !bytes.Equal(restored, before) { t.Fatalf("restored=%s %v", restored, err) }
}
```

- [ ] **Step 2: Run tests and observe missing fields/settings**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/adapter/hermes -run 'TestProviderSendsComplete|TestMergeConfigMakesGroups' -count=1`

Expected: FAIL because the current template collapses user identity and config merge does not own both Session flags.

- [ ] **Step 3: Implement Provider context capture and bounded turn IDs**

The Python Provider stores `_chat_type`, `_chat_id`, `_thread_id`, `_primary_subject`, and `_alternate_subject` from `initialize`. It chooses alternate then primary only for actor turn-ID material, never as canonical user. `turn_id` hashes session, actor subject, counter, user text, and assistant text; request timeouts and queue bounds remain unchanged.

- [ ] **Step 4: Write failing candidate detection tests**

```go
func TestDetectIdentityCandidatesDeduplicatesAndPrefersUnionID(t *testing.T) {
    runner := &fakeOrbRunner{results: map[string][]byte{candidateCommandKey("hermes-agent-env"): []byte(`[
      {"display_name":"陈科良","user_id":"u_owner","user_id_alt":"on_owner","last_seen":10},
      {"display_name":"陈科良","user_id":"u_owner","user_id_alt":"on_owner","last_seen":20},
      {"display_name":"其他用户","user_id":"u_other","user_id_alt":"","last_seen":15}
    ]`)}}
    got, err := DetectIdentityCandidates(context.Background(), runner, "hermes-agent-env", "/home/test/.hermes/state.db", 50)
    if err != nil { t.Fatal(err) }
    if len(got) != 2 || got[0].Kind != "union_id" || got[0].Value != "on_owner" { t.Fatalf("candidates=%#v", got) }
}
```

Implement a direct argv `orb -m <machine> python3 -c <constant-script> <db-path> <limit>` read-only URI query. The script selects only Feishu `origin_json`, orders by activity, caps at 200, and emits JSON. Do not log command stdout. Validate the configured DB path stays inside detected Hermes Home.

`identity.Candidate` implements redacted `String` and `GoString`; test failures may print display name, kind, last-seen time, and suffix but never `Value`.

- [ ] **Step 5: Run and commit Hermes changes**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/adapter/hermes -count=1`

Expected: PASS.

```bash
rtk git add internal/adapter/hermes/templates/__init__.py.tmpl internal/adapter/hermes/plan_test.go internal/adapter/hermes/config.go internal/adapter/hermes/config_test.go internal/adapter/hermes/candidates.go internal/adapter/hermes/candidates_test.go
rtk git commit -m "feat: route hermes chat context into mlink"
```

---

### Task 7: Installer Schema v2, Owner Binding, and Semantic Restore

**Files:**
- Modify: `internal/app/service.go`
- Modify: `internal/app/install.go`
- Modify: `internal/app/install_test.go`
- Modify: `internal/app/restore.go`
- Modify: `internal/app/restore_test.go`
- Modify: `internal/app/uninstall.go`
- Modify: `internal/app/uninstall_test.go`

**Interfaces:**
- Consumes: config v2, identity Repository, Hermes official merge.
- Produces: secret-independent ChangeSet and transactional Apply for owner Principal/Binding.

- [ ] **Step 1: Write failing exact desired-config and cross-process Plan tests**

```go
func TestPlanInstallCreatesThreeSpacesAndStableOwner(t *testing.T) {
    service, _, _ := newInstallFixture(t)
    request := fixtureInstallRequestV2()
    plan, err := service.PlanInstall(context.Background(), request)
    if err != nil { t.Fatal(err) }
    configOp := operationForTarget(t, plan, service.Paths.Config)
    var got config.Config
    if err := yaml.Unmarshal(configOp.Content, &got); err != nil { t.Fatal(err) }
    if got.Principals["owner"].CanonicalUserID != "usr_owner_keliang" || len(got.Spaces) != 3 { t.Fatalf("config=%#v", got) }
    if got.Spaces["personal-owner"].IncludeAgentShared != true || got.Spaces["hermes-groups"].IncludeAgentShared { t.Fatalf("spaces=%#v", got.Spaces) }
}

func TestPlanIdentityDoesNotDependOnTokenBindingOrIdentityKey(t *testing.T) {
    a, _, _ := newInstallFixture(t)
    b, _, _ := newInstallFixture(t)
    a.IdentityKey = bytes.Repeat([]byte{0x11}, 32)
    b.IdentityKey = bytes.Repeat([]byte{0x22}, 32)
    first := fixtureInstallRequestV2(); first.SecretInputs[OwnerBindingSecret] = []byte("on_old")
    second := fixtureInstallRequestV2(); second.SecretInputs[OwnerBindingSecret] = []byte("on_new")
    pa, _ := a.PlanInstall(context.Background(), first)
    pb, _ := b.PlanInstall(context.Background(), second)
    if pa.PlanID != pb.PlanID { t.Fatalf("secret-dependent plans: %s %s", pa.PlanID, pb.PlanID) }
}
```

- [ ] **Step 2: Run App tests and observe v1 desired config**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/app -run 'TestPlanInstallCreatesThree|TestPlanIdentityDoesNotDepend' -count=1`

Expected: FAIL because InstallRequest and desiredConfig still place identity on Connection.

- [ ] **Step 3: Implement v2 InstallRequest and desired graph**

```go
const OwnerBindingSecret = "owner_feishu_binding"
type InstallRequest struct {
    Agents []Agent
    Connection config.Connection
    OwnerSlug string
    OwnerBindingSlot config.BindingRef
    SecretInputs map[string][]byte
    HermesMachine, HermesHome string
}
```

Normalize `OwnerSlug` with the existing safe-ID rule and generate `CanonicalUserID = "usr_owner_" + slug`. Config operations contain only the fixed slot and Keychain ref. Apply stores MemoryCore token, existing-or-new Identity Key, Hermes Grant, and owner binding in one reversible secret transaction before file operations; rollback restores every prior Keychain value.

- [ ] **Step 4: Add restore/uninstall tests for both Session flags and Binding secrets**

```go
func TestFullUninstallRestoresHermesFlagsAndRemovesBinding(t *testing.T) {
    service, target, secrets := newInstallFixture(t)
    request := fixtureInstallRequestV2()
    installPlan, _ := service.PlanInstall(context.Background(), request)
    if err := service.ApplyInstall(context.Background(), installPlan.PlanID, request); err != nil { t.Fatal(err) }
    uninstallPlan, err := service.PlanUninstall(context.Background(), UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}})
    if err != nil { t.Fatal(err) }
    if err := service.ApplyUninstall(context.Background(), uninstallPlan.PlanID, UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}); err != nil { t.Fatal(err) }
    if got := string(target.files[request.HermesHome+"/config.yaml"].content); !strings.Contains(got, "group_sessions_per_user: true") || strings.Contains(got, "thread_sessions_per_user:") { t.Fatalf("restored=%s", got) }
    if _, exists := secrets.values["identity/binding/owner-feishu-union-1"]; exists { t.Fatal("binding secret retained") }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/app -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Installer v2**

```bash
rtk git add internal/app/service.go internal/app/install.go internal/app/install_test.go internal/app/restore.go internal/app/restore_test.go internal/app/uninstall.go internal/app/uninstall_test.go
rtk git commit -m "feat: install stable principal memory spaces"
```

---

### Task 8: Runtime Provider Reuse, Router Construction, and Doctor

**Files:**
- Modify: `cmd/mlink/runtime.go`
- Modify: `cmd/mlink/main_test.go`
- Modify: `cmd/mlink/diagnostics.go`
- Modify: `internal/backend/runtime.go`
- Modify: `internal/backend/runtime_test.go`
- Modify: `internal/doctor/check.go`
- Modify: `internal/doctor/check_test.go`

**Interfaces:**
- Consumes: config v2, Binding Repository, identity Router.
- Produces: one Provider Session per Connection revision shared by all Spaces; identity diagnostics.

- [ ] **Step 1: Write failing Provider-reuse and Router-construction tests**

```go
func TestSpacesOnOneConnectionReuseProviderSession(t *testing.T) {
    starts := 0
    runtime := NewRuntime(RuntimeConfig{Manifest: fixtureManifest(), Secrets: fixtureSecrets(), StartProvider: func(context.Context, host.Config) (host.Session, error) { starts++; return fixtureSession(), nil }})
    connection := fixtureConnectionV2()
    first, err := runtime.Start(context.Background(), connection)
    if err != nil { t.Fatal(err) }
    second, err := runtime.Start(context.Background(), connection)
    if err != nil || first != second || starts != 1 { t.Fatalf("sessions=%p/%p starts=%d err=%v", first, second, starts, err) }
}

func TestRuntimeLoadsOwnerBindingAndThreeSpaces(t *testing.T) {
    configuration := fixtureRuntimeConfigV2()
    router, grants, err := runtimeRouter(context.Background(), configuration, runtimeSecretStore{"identity/hmac-key": bytes.Repeat([]byte{0x2a},32), "identity/binding/owner-feishu-union-1": []byte("on_owner"), "adapter/hermes/token": []byte("grant")})
    if err != nil { t.Fatal(err) }
    if len(grants) != 3 || router == nil { t.Fatalf("router/grants=%#v %#v", router, grants) }
}
```

- [ ] **Step 2: Run focused tests and observe missing v2 runtime**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./cmd/mlink ./internal/backend -run 'TestSpacesOnOne|TestRuntimeLoadsOwner' -count=1`

Expected: FAIL because runtime grants still embed legacy Connection identity.

- [ ] **Step 3: Build the Router before opening listeners**

Broker startup order becomes: load/validate config v2 → read Identity Key → load every active Binding → construct Router → start one Provider Session per referenced Connection → create grants → bind Unix/private listeners. Missing/corrupt bindings or Identity Key fail before any socket is exposed.

- [ ] **Step 4: Add structured Doctor checks**

Add stable checks:

```text
identity.owner: active | binding_missing | binding_conflict
identity.key: active | missing | invalid
spaces.personal: active | invalid
spaces.hermes_private: active | invalid
spaces.hermes_groups: active | invalid
hermes.session_policy: active | group_session_split | thread_session_split
```

```go
func TestDoctorDistinguishesBindingFailureFromHermesBridge(t *testing.T) {
    report := Report{Checks: []Check{{ID:"identity.owner",State:StateFailed,Code:"binding_missing"},{ID:"hermes.bridge",State:StatePassed,Code:"reachable"}}}
    if report.ExitCode()!=1 || report.Checks[0].Code==report.Checks[1].Code { t.Fatalf("report=%#v",report) }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./cmd/mlink ./internal/backend ./internal/doctor -count=1`

Expected: PASS.

- [ ] **Step 5: Commit runtime routing**

```bash
rtk git add cmd/mlink/runtime.go cmd/mlink/main_test.go cmd/mlink/diagnostics.go internal/backend/runtime.go internal/backend/runtime_test.go internal/doctor/check.go internal/doctor/check_test.go
rtk git commit -m "feat: run dynamic mlink memory spaces"
```

---

### Task 9: Identity List, Bind, Rebind, and Revoke CLI

**Files:**
- Create: `internal/app/identity.go`
- Create: `internal/app/identity_test.go`
- Create: `internal/cli/identity.go`
- Create: `internal/cli/identity_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/install.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Consumes: identity Repository and config Store transaction boundary.
- Produces public `mlink identity list|bind|rebind|revoke` workflows.

- [ ] **Step 1: Write failing CLI safety and ChangeSet tests**

```go
func TestIdentityBindReadsValueOnlyFromStdinAndPreviews(t *testing.T) {
    application := &fakeIdentityApplication{plan: fixtureIdentityPlan()}
    stdout := new(bytes.Buffer)
    code := Run(context.Background(), []string{"identity","bind","--principal","owner","--source","feishu","--kind","union_id","--slot","owner-feishu-union-2","--stdin","--dry-run","--json"}, Dependencies{App:application,Stdin:strings.NewReader("on_new\n"),Stdout:stdout,Stderr:io.Discard})
    if code!=0 || application.applyCalls!=0 || bytes.Contains(stdout.Bytes(),[]byte("on_new")) { t.Fatalf("code/apply/output=%d/%d/%s",code,application.applyCalls,stdout.Bytes()) }
}

func TestIdentityRejectsExternalIDInArgv(t *testing.T) {
    code := Run(context.Background(), []string{"identity","bind","--value","on_secret"}, Dependencies{Stderr:io.Discard})
    if code!=2 { t.Fatalf("code=%d",code) }
}
```

Add `mlink install --install-secrets-stdin`: stdin contains one bounded JSON object decoded with unknown fields rejected:

```json
{"memorycore_token":"...","owner_binding":{"kind":"union_id","value":"..."}}
```

The JSON object is never logged or echoed and every decoded byte buffer is wiped after Plan/Apply. The existing memorycore-only stdin option remains valid only when Hermes is not selected.

- [ ] **Step 2: Run tests and observe unsupported identity command**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/cli -run TestIdentity -count=1`

Expected: FAIL with unsupported command.

- [ ] **Step 3: Implement App identity ChangeSets**

```go
type IdentityBindRequest struct { SlotID, Source, Kind, PrincipalID string; Value []byte }
type IdentityRevokeRequest struct { SlotID string }
type IdentityDescriptor struct { SlotID, Source, Kind, PrincipalID, Status, Fingerprint, Suffix string }
```

`PlanIdentityBind/Revoke` renders only semantic operations such as `binding:owner-feishu-union-2 absent -> active`; Apply verifies exact Plan ID, updates config atomically, updates Keychain transactionally, and rolls both back on failure. `IdentityDescriptor` fingerprints/suffixes are computed only after Keychain read and custom JSON marshaling must never include raw Value.

`identity rebind <old-slot> --new-slot <slot> --kind <kind> --stdin` atomically adds the new active Binding and marks the old slot revoked. The old raw value remains only in Keychain so Router can return `ErrBindingRevoked`; it cannot recall or capture any memory and cannot fall back to `hermes-private`.

- [ ] **Step 4: Run App/CLI identity tests**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/app ./internal/cli -count=1`

Expected: PASS with exit `3` for stale/conflicting bindings and `0` for declined confirmation.

- [ ] **Step 5: Commit identity management commands**

```bash
rtk git add internal/app/identity.go internal/app/identity_test.go internal/cli/identity.go internal/cli/identity_test.go internal/cli/run.go internal/cli/install.go internal/cli/run_test.go
rtk git commit -m "feat: manage mlink identity aliases"
```

---

### Task 10: Encrypted Identity Export and Import

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/identity/bundle.go`
- Create: `internal/identity/bundle_test.go`
- Modify: `internal/app/identity.go`
- Modify: `internal/app/identity_test.go`
- Modify: `internal/cli/identity.go`
- Modify: `internal/cli/identity_test.go`

**Interfaces:**
- Consumes: config Principals/Spaces/BindingRefs, Identity Key, raw Binding values.
- Produces: `identity.EncryptBundle`, `DecryptBundle`, App export/import preview and transaction.

- [ ] **Step 1: Pin Go 1.22-compatible scrypt dependency**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go get golang.org/x/crypto@v0.28.0`

Expected: `go.mod` remains `go 1.22`; only the exact compatible module version is added/upgraded.

- [ ] **Step 2: Write failing round-trip, wrong-passphrase, tamper, and permission tests**

```go
func TestEncryptedBundleRoundTripAndTamperDetection(t *testing.T) {
    bundle := BundleV1{SchemaVersion:1, Principals:fixturePrincipals(), Spaces:fixtureSpaces(), IdentityKey:bytes.Repeat([]byte{0x2a},32), Bindings:[]BindingExport{{SlotID:"owner-feishu-union-1",Value:[]byte("on_owner")}}}
    encrypted, err := EncryptBundle(bundle, []byte("passphrase"), bytes.NewReader(bytes.Repeat([]byte{0x11},64)))
    if err != nil { t.Fatal(err) }
    got, err := DecryptBundle(encrypted, []byte("passphrase"))
    if err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(got,bundle) { t.Fatal("decrypted bundle differs") }
    encrypted[len(encrypted)-1] ^= 1
    if _, err := DecryptBundle(encrypted, []byte("passphrase")); !errors.Is(err,ErrBundleAuthentication) { t.Fatalf("tamper err=%v",err) }
}
```

- [ ] **Step 3: Implement the exact bundle envelope**

```go
type encryptedEnvelope struct {
    Format string `json:"format"` // "mlink-identity-bundle/v1"
    Salt string `json:"salt"`
    Nonce string `json:"nonce"`
    Ciphertext string `json:"ciphertext"`
}
```

Use `scrypt.Key(passphrase, salt, 32768, 8, 1, 32)` and AES-256-GCM with authenticated additional data equal to the Format. Require passphrases of at least 12 bytes, a 32-byte Identity Key, unique binding slots, and valid config graph. Wipe plaintext and derived key slices. `WriteBundleAtomic` uses `0600`, fsync, rename, and directory fsync.

`BundleV1`, `BindingExport`, and decrypt errors implement redacted `String`/`GoString`; a failed test or CLI error may show slot metadata but never raw Binding values or Identity Key bytes.

- [ ] **Step 4: Add App/CLI import dry-run and collision tests**

```go
func TestIdentityImportRefusesCanonicalUserCollision(t *testing.T) {
    application := fixtureIdentityApp(t)
    bundle := fixtureBundle(); bundle.Principals["owner"] = config.Principal{ID:"owner",CanonicalUserID:"usr_other",Kind:config.PrincipalPerson}
    _, err := application.PlanIdentityImport(context.Background(), bundle)
    if !errors.Is(err,ErrIdentityImportConflict) { t.Fatalf("err=%v",err) }
}
```

CLI passphrase is read from masked TTY or `--passphrase-stdin`; it is never accepted in argv. Import JSON mode remains non-mutating without `--apply-plan` and `--yes`.

- [ ] **Step 5: Run and commit bundle support**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity ./internal/app ./internal/cli -count=1`

Expected: PASS.

```bash
rtk git add go.mod go.sum internal/identity/bundle.go internal/identity/bundle_test.go internal/app/identity.go internal/app/identity_test.go internal/cli/identity.go internal/cli/identity_test.go
rtk git commit -m "feat: export encrypted mlink identities"
```

---

### Task 11: TUI Owner Selection and Space Preview

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`
- Modify: `cmd/mlink/runtime.go`

**Interfaces:**
- Consumes: `hermes.DetectIdentityCandidates`, v2 InstallRequest.
- Produces: guided owner selection, redacted Binding preview, and three-Space explanation.

- [ ] **Step 1: Write failing selection/no-auto-match tests**

```go
func TestWizardRequiresExplicitOwnerSelection(t *testing.T) {
    application := &fakeApplication{candidates: []identity.Candidate{{DisplayName:"陈科良",Kind:"union_id",Value:[]byte("on_owner"),Suffix:"1234"},{DisplayName:"陈科良",Kind:"user_id",Value:[]byte("u_other"),Suffix:"5678"}}}
    model := New(application, fixtureRequestV2())
    model = driveToIdentity(t,model)
    model = advance(t,model,tea.KeyMsg{Type:tea.KeyEnter})
    if model.step != StepIdentity || application.planCalls != 0 { t.Fatalf("auto-selected duplicate name: step=%d calls=%d",model.step,application.planCalls) }
    model = advance(t,model,tea.KeyMsg{Type:tea.KeySpace})
    model = advance(t,model,tea.KeyMsg{Type:tea.KeyEnter})
    if model.request.OwnerBindingSlot.ID == "" { t.Fatal("selected binding missing") }
}
```

- [ ] **Step 2: Run TUI tests and observe missing candidate state**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/tui -run TestWizardRequiresExplicit -count=1`

Expected: FAIL because the Identity screen shows static IDs and has no candidate selection.

- [ ] **Step 3: Implement masked candidate selection and Space summary**

The Identity screen lists display name, source kind, last-seen time, and last four characters. It never renders full Value. Selecting one candidate copies Value only into masked model memory and generates deterministic slots `owner-feishu-<kind>-1`. Preview renders:

```text
personal-owner   Codex · Pi · selected Feishu DM    L1/L2/L3
hermes-private   other Feishu DMs                   L1
hermes-groups    Feishu groups and topics           group L1
```

After Apply, decline, error, or Quit, overwrite every candidate Value and SecretInputs buffer.

- [ ] **Step 4: Run width, leak, and confirmation tests**

```go
func TestViewNeverRendersRawCandidateID(t *testing.T) {
    model := fixtureCandidateModel("on_actual_union_id")
    for _, width := range []int{72,100,140} {
        model.width=width
        if strings.Contains(model.View(),"on_actual_union_id") { t.Fatalf("width %d leaked ID",width) }
    }
}
```

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/tui ./cmd/mlink -count=1`

Expected: PASS; every line remains within its terminal width.

- [ ] **Step 5: Commit TUI identity setup**

```bash
rtk git add internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/tui/view_test.go cmd/mlink/runtime.go
rtk git commit -m "feat: guide owner identity and memory spaces"
```

---

### Task 12: Offline End-to-End and Adversarial Acceptance

**Files:**
- Create: `internal/e2e/identity_test.go`
- Create: `internal/e2e/group_routing_test.go`
- Create: `internal/e2e/identity_bundle_test.go`
- Create: `internal/e2e/secret_leak_test.go`
- Modify: `internal/e2e/lifecycle_test.go`
- Create: `docs/testing/mlink-principal-routing-isolated.md`

**Interfaces:**
- Consumes all Tasks 1–11 public App/Broker/CLI/TUI interfaces.
- Produces deterministic evidence before any real state mutation.

- [ ] **Step 1: Add failing table-driven identity and group cases**

```go
func TestOfflineIdentityAcceptance(t *testing.T) {
    fixture := NewFixture(t)
    cases := []struct{name string; context identity.ExternalContext; wantSpace,wantUser string; wantShared bool}{
        {"owner old alias",dm("on_old"),"personal-owner",fixture.OwnerUser(),true},
        {"owner new alias",dm("on_new"),"personal-owner",fixture.OwnerUser(),true},
        {"unbound dm",dm("on_other"),"hermes-private","",false},
        {"group member a",group("oc_g","","on_a"),"hermes-groups","",false},
        {"group member b",group("oc_g","","on_b"),"hermes-groups","",false},
        {"topic member a",group("oc_g","omt_t","on_a"),"hermes-groups","",false},
        {"topic member b",group("oc_g","omt_t","on_b"),"hermes-groups","",false},
    }
    for _,tc:=range cases { t.Run(tc.name,func(t *testing.T){ fixture.AssertRoute(t,tc.context,tc.wantSpace,tc.wantUser,tc.wantShared) }) }
    fixture.AssertGroupMembersSharePrincipal(t,"oc_g")
    fixture.AssertTopicsShareGroupButNotSession(t,"oc_g","omt_a","omt_b")
}
```

- [ ] **Step 2: Add replay, restart, revoke, rollback, and raw-ID leak cases**

Cover exactly:

```text
old/new owner aliases interleaved 100 times
same group topic from two users interleaved 100 times
same turn replayed 10 times
same turn ID with changed content
group event with same turn ID from two actor digests
missing chat_id, missing subject, unsupported chat_type
card action without thread_id
Broker restart with Keychain reload
binding add failure after config write
config write failure after Keychain snapshot
revoke old alias while new remains active
identity bundle wrong passphrase and one-bit tamper
import canonical-user collision
raw IDs placed in external-context fields, malformed candidate fields, error causes, and routing requests
```

The leak test walks rendered JSON/text, Journal database bytes, logs, backup metadata, and config YAML and fails on the raw external canaries. It does not scan the encrypted bundle ciphertext or Keychain fake store because those are the authorized locations.

- [ ] **Step 3: Run the E2E suite and fix owning packages only**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/e2e -count=1`

Expected: FAIL until every case is implemented; fixes belong to config/identity/broker/journal/app/adapter packages, not conditional exceptions in the fixture.

- [ ] **Step 4: Run the full offline matrix and write evidence**

Run:

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./...
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test -shuffle=on -count=2 ./...
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go vet ./...
rtk env -u GOROOT GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 /opt/homebrew/bin/go test ./...
rtk env -u GOROOT /opt/homebrew/bin/go test -race ./internal/identity ./internal/broker ./internal/journal ./internal/e2e
```

Expected: all non-race commands PASS. Race PASS is required from the project but may be environment-blocked by the known Xcode architecture error; record the exact compiler error without changing system Xcode settings.

Write `docs/testing/mlink-principal-routing-isolated.md` with command output summaries, case counts, and the distinction between MLink alias guarantees and MemoryCore semantic behavior.

- [ ] **Step 5: Commit offline acceptance**

```bash
rtk git add internal/e2e/identity_test.go internal/e2e/group_routing_test.go internal/e2e/identity_bundle_test.go internal/e2e/secret_leak_test.go internal/e2e/lifecycle_test.go docs/testing/mlink-principal-routing-isolated.md
rtk git commit -m "test: verify principal routing and alias safety"
```

Before this commit, inspect `rtk git status --short` and stop if the user-owned `README.md` is staged; do not use broad staging or mutate that file.

---

### Task 13: Isolated Real MemoryCore Alias and Space Acceptance

**Files:**
- Create: `internal/e2e/live_identity_test.go`
- Create: `docs/testing/mlink-principal-routing-live.md`

**Interfaces:**
- Consumes the local isolated `mlink-memorycore-test:v2.0.1` instance only.
- Produces the ten mandatory identity-continuity results without touching normal identities.

- [ ] **Step 1: Display and validate the exact isolated write scope**

Use a run ID such as `mlink-alias-<unix-nanos>` and display only:

```text
base_url: http://127.0.0.1:8420
service_id: <run-id>
team_id: team-<run-id>
owner agent_id: owner-<run-id>
private agent_id: private-<run-id>
group agent_id: groups-<run-id>
canonical owner user_id: usr-owner-<run-id>
```

Read the existing Gateway token from the Docker container environment into the test process without printing it or placing it in argv. Abort if container name/image/loopback port differ from the documented isolated target.

- [ ] **Step 2: Write the live acceptance test before running it**

```go
func TestLiveAliasRotationWithoutMemoryMigration(t *testing.T) {
    fixture := newLiveRoutingFixture(t)
    canary := fixture.SeedOwnerThroughAlias(t,"on_old")
    fixture.EventuallyRecallOwner(t,"codex",canary)
    fixture.EventuallyRecallOwner(t,"pi",canary)
    fixture.EventuallyRecallOwner(t,"on_old",canary)
    before := fixture.OwnerInventory(t)
    fixture.BindOwnerAlias(t,"on_new")
    fixture.RestartBroker(t)
    fixture.EventuallyRecallOwner(t,"on_new",canary)
    fixture.RevokeAlias(t,"on_old")
    fixture.AssertAliasRejected(t,"on_old")
    fixture.EventuallyRecallOwner(t,"on_new",canary)
    fixture.AssertUnboundCannotRecall(t,"on_other",canary)
    fixture.AssertGroupCannotRecall(t,"oc_group",canary)
    fixture.ExportResetImportIdentity(t)
    fixture.EventuallyRecallOwner(t,"on_new",canary)
    after := fixture.OwnerInventory(t)
    if before.CanonicalUserID != after.CanonicalUserID { t.Fatalf("canonical identity changed") }
    fixture.AssertAllBoundRequestsUsedCanonicalOwner(t)
    fixture.AssertCanaryAbsentFromDerivedAliasUsers(t,canary,"on_old","on_new")
}
```

- [ ] **Step 3: Run the live identity test**

Run shape:

```bash
rtk docker inspect mlink-memorycore-test | rtk jq -r '.[0].Config.Env[] | select(startswith("TDAI_GATEWAY_API_KEY=")) | split("=")[1:] | join("=")' | rtk sh -c 'IFS= read -r mlink_live_token; MLINK_TEST_MEMORYCORE_TOKEN="$mlink_live_token" MLINK_TEST_MEMORYCORE_URL=http://127.0.0.1:8420 CGO_ENABLED=0 env -u GOROOT /opt/homebrew/bin/go test -tags=integration ./internal/e2e -run TestLiveAliasRotationWithoutMemoryMigration -count=1 -v -timeout=8m'
```

Expected: PASS all ten mandatory gates. L1 extraction may be asynchronous; poll with bounded 2-second intervals and an 180-second deadline. Never retry an ambiguous non-replay-safe capture.

- [ ] **Step 4: Run isolated group separation**

Add and run a second test proving owner, unbound DM, group A, and group B cannot recall one another's canaries; two topics in group A recall the group-A canary under the same group Principal but have different Session IDs. Confirm all group/private requests use `include_agent_shared=false` and owner uses `true`.

- [ ] **Step 5: Record and commit live evidence**

Write `docs/testing/mlink-principal-routing-live.md` with unique non-secret IDs, elapsed extraction time, canonical identity before/after hashes, counts, and pass/fail for each mandatory gate. Do not include raw aliases, chat IDs, topic IDs, tokens, or memory text.

```bash
rtk git add internal/e2e/live_identity_test.go docs/testing/mlink-principal-routing-live.md
rtk git commit -m "test: validate alias continuity against memorycore"
```

---

### Task 14: Final Real-Machine Preview and Approval Gate

**Files:**
- Replace: `docs/testing/mlink-user-layer-real-preview.md`

**Interfaces:**
- Consumes the final verified candidate and current real Agent state read-only.
- Produces a new exact ChangeSet; performs no installation.

- [ ] **Step 1: Build and hash a new fixed candidate**

Run: `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go build -trimpath -o /tmp/mlink-principal-routing-preview ./cmd/mlink`

Expected: build succeeds; record SHA-256; installed `~/.local/bin/mlink` remains absent or unchanged.

- [ ] **Step 2: Capture protected hashes and candidate identities**

Hash Codex model config, Pi settings, Hermes config, and any existing MLink files. Query Hermes candidates read-only and display only name/kind/suffix so the user can confirm the owner slot. Confirm current real group and topic IDs remain raw-secret inputs and never enter the report.

- [ ] **Step 3: Generate the final dry-run**

Run the candidate with MemoryCore token and selected owner Binding supplied through the single `--install-secrets-stdin` bounded JSON channel supported by the final CLI/TUI. Preview must show:

```text
schema v2
one TencentDB Connection
personal-owner / hermes-private / hermes-groups Spaces
owner Principal canonical ID
redacted owner Binding slot
Codex/Pi fixed owner route
Hermes dynamic route
group_sessions_per_user: true -> false
thread_sessions_per_user: default false -> explicit false
Hermes built-in memory/profile/tool shutdown
all model/auth protected invariants preserved
```

- [ ] **Step 4: Prove preview is reproducible and zero-write**

Run the preview in a second process and require identical Plan ID. Recheck file hashes, Keychain item-not-found/status, LaunchAgent, Socket, Journal, Codex Hook, Pi Extension, Hermes Provider, Binding accounts, and encrypted bundle paths. Any change fails the gate.

- [ ] **Step 5: Replace the preview report and stop**

Update `docs/testing/mlink-user-layer-real-preview.md`, explicitly mark `plan_5b496cbe0ed3a1c09525934f9c` invalid, and record the new Plan ID/operations/invariants without secrets.

```bash
rtk git add docs/testing/mlink-user-layer-real-preview.md
rtk git commit -m "docs: record principal routing install preview"
```

Show the exact new ChangeSet to the user and stop. Do not Apply until the user explicitly confirms that new Plan ID after seeing the preview.

---

## Final Self-Review Checklist

- Tasks 1–2 define every config/identity type used later.
- Task 3 keeps raw aliases only in Keychain and memory.
- Task 4 authenticates before dynamic routing and drops raw context before Service/Provider.
- Task 5 prevents group-actor turn collisions without persisting raw actor IDs.
- Task 6 uses official Hermes fields and restores both Session flags.
- Tasks 7–8 preserve zero-write preview, Provider reuse, Keychain rollback, and runtime fail-closed behavior.
- Tasks 9–11 expose reversible CLI/TUI identity workflows without argv/plaintext leaks.
- Task 12 covers offline replay, conflicts, missing fields, rollback, and secret scanning.
- Task 13 proves all ten user-approved alias-continuity gates against the isolated backend.
- Task 14 replaces the obsolete real-machine Plan and stops for fresh confirmation.
- No task modifies Agent model configuration, Hermes source, TencentDB source, HyMemory data, `MEMORY.md`, `USER.md`, or the user's unstaged `README.md`.
