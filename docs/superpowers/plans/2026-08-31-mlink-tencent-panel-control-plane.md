# MLink TencentDB Panel Control Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make TencentDB MemoryCore the source of truth for MLink User, Team, Agent, and Asset IDs; install the official loopback-only MemoryPanel; and dynamically provision one backend Agent per non-Owner Feishu DM principal and per Feishu group.

**Architecture:** A two-stage installer first provisions TencentDB metadata and the Panel without changing active memory routing, then generates a second exact ChangeSet that cuts MLink over to the returned IDs. Runtime identity resolution emits a principal intent; a fail-closed provisioner reconciles that intent to a Core-generated Agent and persists the mapping in the Journal before recall or capture.

**Tech Stack:** Go 1.22 with CGO disabled, SQLite Journal, macOS Keychain, Docker/OrbStack, TencentDB MemoryCore v3 metadata APIs, official MemoryPanel Node.js 22/React image pinned at commit `a5dcbe6`.

**Spec:** `docs/superpowers/specs/2026-08-31-mlink-tencent-panel-control-plane-design.md`

## Global Constraints

- Start every repository shell command with `rtk`.
- Run Go commands as `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go ...` unless a test explicitly sets `GOTOOLCHAIN=go1.22.12`.
- Remain on `feat_product_design`; the user explicitly declined a worktree.
- Preserve the user's unstaged `README.md`; never stage it.
- Use strict TDD: failing test, observed failure, minimal implementation, passing test, explicit-file commit.
- Do not change Codex, Pi, or Hermes model providers, model URLs, subscriptions, or authentication.
- Do not deploy or configure port 8096 MemoryProxy or Knowledge Service.
- Bind Panel only as `127.0.0.1:8125 -> 8123/tcp`.
- Build official Panel source from pinned commit `a5dcbe6`; do not use an unpinned image tag.
- Do not migrate, delete, rewrite, or merge existing MemoryCore data under `personal / keliang-personal / usr_owner_keliang`.
- Never persist raw Feishu `union_id`, `user_id`, `open_id`, `chat_id`, or topic ID outside Keychain or request-local memory.
- Never place Gateway Bearer, admin `user_key`, Owner `user_key`, identity HMAC key, or Hermes grant in argv, logs, Git, ChangeSet JSON, or unencrypted backups.
- Core may generate L2/L3 for private/group Agents; MLink runtime must never recall or inject those layers for private/group routes.
- Stage A and Stage B are separate mutations with separate exact confirmation gates.
- A failed dynamic provision must not fall back to Owner, shared-private, or shared-group memory.

---

### Task 1: Schema v3 Control Plane and Routing Policies

**Files:**
- Modify: `internal/config/types.go`
- Modify: `internal/config/store.go`
- Modify: `internal/config/store_test.go`
- Modify: `internal/config/migrate.go`
- Modify: `internal/config/migrate_test.go`

**Interfaces:**
- Produces: `config.ControlPlane`, `config.RoutingPolicy`, `config.DynamicAgentPolicy`, Schema v3 validation.
- Preserves: Schema v2 load for the currently installed runtime until Stage B.

- [ ] **Step 1: Write failing Schema v3 round-trip and validation tests**

Add fixtures that require exact generated IDs and layer policies:

```go
func fixtureV3Config() Config {
    return Config{
        SchemaVersion: 3,
        NamespaceID: "installation-1",
        ActiveConnectionID: "local",
        ControlPlane: &ControlPlane{
            ProviderID: "dev.mlink.tencentdb",
            InstanceID: "default",
            PanelURL: "http://127.0.0.1:8125",
            OwnerUserID: "usr-generated",
            OwnerTeamID: "team-generated",
            OwnerAgentID: "agt-owner",
            OwnerAssetID: "chat_memory-team-generated-agt-owner",
        },
        RoutingPolicies: map[string]RoutingPolicy{
            "owner": {ID: "owner", Layers: []MemoryLayer{LayerL1, LayerL2, LayerL3}, AgentPolicy: AgentFixed},
            "hermes-private": {ID: "hermes-private", Layers: []MemoryLayer{LayerL1}, AgentPolicy: AgentDynamicPrincipal},
            "hermes-groups": {ID: "hermes-groups", Layers: []MemoryLayer{LayerL1}, AgentPolicy: AgentDynamicGroup, SessionPolicy: SessionPerTopic},
        },
    }
}
```

Assert that missing generated IDs, non-loopback `panel_url`, L2/L3 in a private/group runtime policy, or a non-TencentDB control-plane provider fails validation. Assert Schema v2 continues to load unchanged.

- [ ] **Step 2: Run config tests and observe failure**

Run:

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/config -run 'Test(StoreRoundTripsV3ControlPlane|ValidateV3RoutingPolicies|LoadV2WithoutImplicitControlPlaneMigration)' -count=1
```

Expected: compile failure for undefined v3 types.

- [ ] **Step 3: Implement the minimal Schema v3 types**

Add these public types:

```go
type MemoryLayer string
const (
    LayerL1 MemoryLayer = "L1"
    LayerL2 MemoryLayer = "L2"
    LayerL3 MemoryLayer = "L3"
)

type AgentPolicy string
const (
    AgentFixed AgentPolicy = "fixed"
    AgentDynamicPrincipal AgentPolicy = "dynamic_per_principal"
    AgentDynamicGroup AgentPolicy = "dynamic_per_group"
)

type SessionPolicy string
const SessionPerTopic SessionPolicy = "per_topic"

type ControlPlane struct {
    ProviderID   string `yaml:"provider_id"`
    InstanceID   string `yaml:"instance_id"`
    PanelURL     string `yaml:"panel_url"`
    OwnerUserID  string `yaml:"owner_user_id"`
    OwnerTeamID  string `yaml:"owner_team_id"`
    OwnerAgentID string `yaml:"owner_agent_id"`
    OwnerAssetID string `yaml:"owner_asset_id"`
}

type RoutingPolicy struct {
    ID            string        `yaml:"id"`
    Layers        []MemoryLayer `yaml:"layers"`
    AgentPolicy   AgentPolicy   `yaml:"agent_policy"`
    SessionPolicy SessionPolicy `yaml:"session_policy,omitempty"`
}
```

Add `ControlPlane *ControlPlane` and `RoutingPolicies map[string]RoutingPolicy` to `Config`. Keep Schema v2 validation intact; never synthesize backend IDs in `migrate.go`.

- [ ] **Step 4: Run config tests**

Run the command from Step 2 and then:

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/config/types.go internal/config/store.go internal/config/store_test.go internal/config/migrate.go internal/config/migrate_test.go
rtk git commit -m "feat: define tencent control plane schema"
```

---

### Task 2: TencentDB Metadata Client

**Files:**
- Create: `internal/provider/tencentdb/metadata.go`
- Create: `internal/provider/tencentdb/metadata_test.go`
- Modify: `internal/provider/tencentdb/client.go`
- Modify: `internal/provider/tencentdb/client_test.go`

**Interfaces:**
- Consumes: existing `tencentdb.Client` Gateway Bearer and Service ID.
- Produces: `MetadataClient` with typed official v3 methods and redacted errors.

- [ ] **Step 1: Write failing metadata contract tests**

Create an HTTP test server and assert headers, paths, request fields, unknown-field rejection, response bounds, and secret redaction for:

```go
type MetadataClient interface {
    InitAdmin(context.Context, InitAdminRequest) (UserCredential, error)
    CreateUser(context.Context, string, CreateUserRequest) (UserCredential, error)
    CreateTeam(context.Context, string, CreateTeamRequest) (Team, error)
    CreateAgent(context.Context, string, CreateAgentRequest) (Agent, error)
    ListAgents(context.Context, string, ListAgentsRequest) ([]Agent, error)
    GetAsset(context.Context, string, string) (Asset, error)
    InstanceQuota(context.Context, string) (InstanceQuota, error)
}

type UserCredential struct {
    UserID string
    UserKey []byte
}
```

Tests require Gateway Bearer in `Authorization`, Service ID in `x-tdai-service-id`, and normal/admin `user_key` only in `x-tdai-user-key`. No credential may appear in `APIError.Error()`.

- [ ] **Step 2: Run metadata tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/provider/tencentdb -run 'TestMetadata' -count=1
```

Expected: compile failure for undefined metadata client types.

- [ ] **Step 3: Implement typed metadata methods**

Add a bounded `postWithUserKey` helper without changing existing data-plane behavior:

```go
func (c *Client) postWithUserKey(ctx context.Context, path string, userKey []byte, body, out any) error
```

Implement official endpoints:

```text
/v3/internal/meta/user/init-admin
/v3/meta/user/create
/v3/meta/team/create
/v3/meta/agent/create
/v3/meta/agent/list
/v3/meta/asset/get
/v3/meta/instance-quota/get
```

Wipe response credential buffers on decode errors and provide `Wipe()` on `UserCredential`.

- [ ] **Step 4: Run metadata and provider tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/provider/tencentdb -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/provider/tencentdb/metadata.go internal/provider/tencentdb/metadata_test.go internal/provider/tencentdb/client.go internal/provider/tencentdb/client_test.go
rtk git commit -m "feat: add tencent metadata client"
```

---

### Task 3: Control-Plane and Principal-Agent Journal State

**Files:**
- Create: `internal/journal/migrations/004_control_plane_agents.sql`
- Create: `internal/journal/control_plane.go`
- Create: `internal/journal/control_plane_test.go`
- Modify: `internal/journal/store.go`
- Modify: `internal/journal/store_test.go`

**Interfaces:**
- Produces: `journal.ControlPlaneState`, `journal.PrincipalAgent`, repositories used by provisioning and restore.
- Guarantees: unique principal fingerprint; no raw external ID columns.

- [ ] **Step 1: Write failing migration and repository tests**

Require these records:

```go
type ControlPlaneState struct {
    InstallationID string
    InstanceID string
    OwnerUserID string
    OwnerTeamID string
    OwnerAgentID string
    OwnerAssetID string
    PanelContainer string
    PanelImage string
    State string
}

type PrincipalAgent struct {
    Fingerprint string
    RouteKind string
    BackendUserID string
    BackendTeamID string
    BackendAgentID string
    BackendAssetID string
    DisplayLabel string
    State string
}
```

Tests cover insert, exact reuse, conflicting IDs, concurrent insert, state transition, list/export, and a scan proving forbidden raw identifiers are absent from stored columns.

- [ ] **Step 2: Run Journal tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/journal -run 'Test(ControlPlane|PrincipalAgent)' -count=1
```

Expected: missing migration/table failures.

- [ ] **Step 3: Add migration 004 and repositories**

Create `control_plane_installations` and `principal_agents`. Make `principal_fingerprint` the primary key, restrict `route_kind` to `hermes-private` or `hermes-group`, and index `backend_agent_id`. Implement repository methods:

```go
SaveControlPlane(context.Context, ControlPlaneState) error
LoadControlPlane(context.Context) (ControlPlaneState, error)
MarkControlPlaneState(context.Context, string) error
PutPrincipalAgent(context.Context, PrincipalAgent) error
GetPrincipalAgent(context.Context, string) (PrincipalAgent, error)
ListPrincipalAgents(context.Context) ([]PrincipalAgent, error)
```

- [ ] **Step 4: Run Journal tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/journal -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/journal/migrations/004_control_plane_agents.sql internal/journal/control_plane.go internal/journal/control_plane_test.go internal/journal/store.go internal/journal/store_test.go
rtk git commit -m "feat: persist tencent control plane mappings"
```

---

### Task 4: Stage A Control-Plane Provisioning

**Files:**
- Create: `internal/controlplane/service.go`
- Create: `internal/controlplane/service_test.go`
- Create: `internal/controlplane/types.go`
- Modify: `internal/app/service.go`
- Modify: `cmd/mlink/runtime.go`

**Interfaces:**
- Consumes: `tencentdb.MetadataClient`, Keychain, Journal control-plane repository.
- Produces: `ProvisionIntent`, `ProvisionResult`, exact reusable state for Stage B.

- [ ] **Step 1: Write failing two-stage provisioning tests**

Define:

```go
type ProvisionRequest struct {
    InstallationID string
    InstanceID string
    AdminUsername string
    OwnerUsername string
    TeamName string
    OwnerAgentName string
}

type ProvisionResult struct {
    PlanID string
    OwnerUserID string
    OwnerTeamID string
    OwnerAgentID string
    OwnerAssetID string
}
```

Tests prove:

- preview performs zero metadata or Keychain writes;
- empty metadata invokes admin bootstrap, then creates a distinct normal Owner;
- existing validated state is reused;
- an initialized backend without valid credentials fails closed;
- admin and Owner keys are stored in separate accounts;
- a failure after Core create but before Journal write is recoverable without duplicate Owner Agents;
- Stage A never edits active MLink config.

- [ ] **Step 2: Run control-plane tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane -count=1
```

Expected: package or symbols missing.

- [ ] **Step 3: Implement provisioning service**

Use Keychain accounts:

```text
control/tencentdb/admin-user-key
control/tencentdb/owner-user-key
```

Build a secret-independent Stage A Plan ID from instance, requested resource names, pinned Panel identity, and current control-plane state. Apply validates the same intent immediately before mutation. Persist returned IDs only after validating ownership, Team membership, Agent owner, and Chat Memory Asset identity.

- [ ] **Step 4: Run control-plane and app tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane ./internal/app ./cmd/mlink -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/controlplane/service.go internal/controlplane/service_test.go internal/controlplane/types.go internal/app/service.go cmd/mlink/runtime.go
rtk git commit -m "feat: provision tencent control plane"
```

---

### Task 5: Official Panel-Only Docker Manager

**Files:**
- Create: `internal/panel/runtime.go`
- Create: `internal/panel/runtime_test.go`
- Create: `internal/panel/registry.go`
- Create: `internal/panel/registry_test.go`
- Modify: `internal/layout/paths.go`
- Modify: `internal/layout/paths_test.go`
- Modify: `internal/controlplane/service.go`
- Modify: `internal/controlplane/service_test.go`

**Interfaces:**
- Produces: `panel.Runtime` with `Plan`, `Apply`, `Status`, and `OpenURL`.
- Uses: pinned source checkout, Docker command runner, protected instance-registry writer.

- [ ] **Step 1: Write failing Panel plan and registry tests**

Require:

```go
const ContainerName = "mlink-memory-panel"
const ImageName = "mlink-memory-panel:a5dcbe6"

type Desired struct {
    SourceRoot string
    RegistryPath string
    HostAddress string
    HostPort int
    ContainerPort int
}
```

Tests assert exact Docker build/run commands, `127.0.0.1:8125:8123`, read-only registry mount, `KNOWLEDGE_LLM_BINDING_SYNC=false`, no 8096/model environment, no secret in argv or ChangeSet, registry mode 0600, and host endpoint translated to `host.docker.internal:8420`.

- [ ] **Step 2: Run Panel tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/panel -count=1
```

Expected: package missing.

- [ ] **Step 3: Implement Panel runtime and protected registry**

Extend layout with:

```text
~/.mlink/panel/
~/.mlink/panel/metadata-instances.json
```

Render the registry only during Apply. Keep its content out of the installation ledger and ordinary backups. On failure, restore the previous file from an in-memory protected buffer or remove the newly created file. Health requires both `/health` and the public instance listing.

- [ ] **Step 4: Run Panel and control-plane tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/panel ./internal/controlplane ./internal/layout -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/panel/runtime.go internal/panel/runtime_test.go internal/panel/registry.go internal/panel/registry_test.go internal/layout/paths.go internal/layout/paths_test.go internal/controlplane/service.go internal/controlplane/service_test.go
rtk git commit -m "feat: manage official memory panel"
```

---

### Task 6: Dynamic Principal-Agent Provisioner

**Files:**
- Create: `internal/controlplane/agents.go`
- Create: `internal/controlplane/agents_test.go`
- Modify: `internal/provider/tencentdb/metadata.go`
- Modify: `internal/provider/tencentdb/metadata_test.go`

**Interfaces:**
- Produces: `AgentProvisioner.ResolveOrCreate(context.Context, PrincipalIntent) (journal.PrincipalAgent, error)`.
- Consumes: Owner control-plane state, metadata client, Journal mapping repository.

- [ ] **Step 1: Write failing reconciliation and concurrency tests**

Define:

```go
type PrincipalIntent struct {
    Fingerprint string
    RouteKind string
    DisplayLabel string
}
```

The provisioner also requires an explicit positive local dynamic-Agent limit because the pinned official Core quota response has no Agent-count field. Reconciliation of an existing remote marker occurs before applying this limit.

Tests cover mapping hit, metadata reconciliation, first create, two concurrent creates, timeout, quota failure, wrong owner/team response, duplicate metadata marker, create success followed by local persistence failure, restart reconciliation, and forbidden raw-ID scanning.

- [ ] **Step 2: Run provisioner tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane -run 'TestAgentProvisioner' -count=1
```

Expected: undefined provisioner.

- [ ] **Step 3: Implement fail-closed provisioner**

Use a keyed single-flight map. Before create, check local mapping, then list Owner Agents and parse their MLink metadata marker. Create the Agent with the normal Owner key and wait for the official auto-minted Asset. Persist mapping before returning.

Use stable readable names containing only route kind, sanitized display label, and a fingerprint suffix. Never include a raw external identifier.

- [ ] **Step 4: Run provisioner and metadata tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane ./internal/provider/tencentdb -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/controlplane/agents.go internal/controlplane/agents_test.go internal/provider/tencentdb/metadata.go internal/provider/tencentdb/metadata_test.go
rtk git commit -m "feat: provision dynamic memory agents"
```

---

### Task 7: Identity Router Intent Model

**Files:**
- Modify: `internal/identity/types.go`
- Modify: `internal/identity/router.go`
- Modify: `internal/identity/router_test.go`
- Modify: `internal/identity/bindings_test.go`

**Interfaces:**
- Produces: `identity.RouteIntent`; fixed Owner resolution remains synchronous.
- Removes: dynamic HMAC values as final TencentDB User IDs.

- [ ] **Step 1: Write failing route-intent tests**

Define:

```go
type RouteKind string
const (
    RouteOwner RouteKind = "owner"
    RoutePrivate RouteKind = "hermes-private"
    RouteGroup RouteKind = "hermes-group"
)

type RouteIntent struct {
    Kind RouteKind
    PrincipalFingerprint string
    SessionID string
    ActorDigest string
    IncludeAgentShared bool
}
```

Assert Owner aliases emit `RouteOwner` with no dynamic fingerprint; unbound DMs emit a stable private fingerprint; groups emit a stable group fingerprint; group topics change Session ID but not fingerprint; no returned field contains the raw external ID.

- [ ] **Step 2: Run identity tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity -run 'TestRouterEmits' -count=1
```

Expected: undefined route-intent types.

- [ ] **Step 3: Implement intent resolution**

Add:

```go
func (router Router) ResolveHermesIntent(context ExternalContext) (RouteIntent, error)
```

Keep `ResolveFixed` for Codex/Pi. Stop constructing dynamic TencentDB `usr_...` and `grp_...` User IDs. Fingerprints remain HMAC values but are used only for Agent lookup.

- [ ] **Step 4: Run identity tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/identity/types.go internal/identity/router.go internal/identity/router_test.go internal/identity/bindings_test.go
rtk git commit -m "refactor: resolve hermes routing intents"
```

---

### Task 8: Broker Authorization and Runtime Dynamic Mapping

**Files:**
- Modify: `internal/broker/http.go`
- Modify: `internal/broker/http_test.go`
- Modify: `internal/broker/service.go`
- Modify: `internal/broker/service_test.go`
- Modify: `cmd/mlink/runtime.go`
- Modify: `cmd/mlink/main_test.go`

**Interfaces:**
- Consumes: `identity.RouteIntent`, dynamic `AgentProvisioner`, Schema v3 control-plane state.
- Produces: final `identity.ResolvedIdentity` with Core-generated IDs.

- [ ] **Step 1: Write failing runtime authorization tests**

Tests require:

- Owner routes directly to generated Owner IDs with L1/L2/L3 enabled.
- Private/group routes call the provisioner before recall/capture.
- private/group identities use Owner User and Team plus mapped Agent.
- private/group set `IncludeAgentShared=false`.
- provision failure returns a non-secret warning and performs zero provider calls.
- no raw context remains after authorization.

- [ ] **Step 2: Run broker/runtime tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/broker ./cmd/mlink -run 'Test.*DynamicAgent' -count=1
```

Expected: runtime still attempts fixed dynamic Spaces.

- [ ] **Step 3: Implement async dynamic authorization**

Refactor the Hermes grant resolver to call `ResolveHermesIntent`, then the provisioner for private/group intents. Construct final identities as:

```go
ResolvedIdentity{
    ConnectionID: activeConnection,
    TenantID: control.OwnerTeamID,
    AgentID: mapping.BackendAgentID,
    UserID: control.OwnerUserID,
    SessionID: intent.SessionID,
    IncludeAgentShared: intent.Kind == RouteOwner,
    ActorDigest: intent.ActorDigest,
}
```

Do not enqueue Journal capture events when provisioning fails.

- [ ] **Step 4: Run broker and runtime tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/broker ./cmd/mlink -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/broker/http.go internal/broker/http_test.go internal/broker/service.go internal/broker/service_test.go cmd/mlink/runtime.go cmd/mlink/main_test.go
rtk git commit -m "feat: route through generated memory agents"
```

---

### Task 9: Stage B No-Migration Cutover

**Files:**
- Create: `internal/app/control_plane_cutover.go`
- Create: `internal/app/control_plane_cutover_test.go`
- Modify: `internal/app/install.go`
- Modify: `internal/app/install_test.go`
- Modify: `internal/config/store.go`

**Interfaces:**
- Consumes: persisted Stage A `ControlPlaneState`.
- Produces: exact Stage B ChangeSet and Schema v3 active config.

- [ ] **Step 1: Write failing cutover tests**

Tests require a secret-independent exact Plan containing:

```text
schema v2 -> schema v3
legacy personal-owner IDs -> Core-generated Owner IDs
dynamic private/group routing policies enabled
legacy_scope_migration -> disabled
Broker restart
Hermes restart
all model/auth invariants preserved
```

Assert the plan contains no operation that reads, copies, deletes, or writes old MemoryCore L0-L3. Assert existing Codex Hook content is unchanged.

- [ ] **Step 2: Run cutover tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/app -run 'TestControlPlaneCutover' -count=1
```

Expected: cutover service missing.

- [ ] **Step 3: Implement exact cutover Plan and Apply**

Add:

```go
func (s *Service) PlanControlPlaneCutover(context.Context, ControlPlaneCutoverRequest) (install.ChangeSet, error)
func (s *Service) ApplyControlPlaneCutover(context.Context, string, ControlPlaneCutoverRequest) error
```

Build Schema v3 exclusively from returned Core IDs. Back up config and service state before change. Recompute the Plan immediately before Apply. The post-apply verifier rejects any legacy active Tenant/Agent/User ID.

- [ ] **Step 4: Run app/config tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/app ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/app/control_plane_cutover.go internal/app/control_plane_cutover_test.go internal/app/install.go internal/app/install_test.go internal/config/store.go
rtk git commit -m "feat: cut over to generated memory ids"
```

---

### Task 10: CLI and TUI Two-Stage Workflow

**Files:**
- Create: `internal/cli/panel.go`
- Create: `internal/cli/panel_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`
- Modify: `cmd/mlink/runtime.go`

**Interfaces:**
- Produces CLI: `mlink panel provision|cutover|status|open|doctor`.
- Produces TUI stages with separate confirmation screens and actual generated-ID previews.

- [ ] **Step 1: Write failing CLI/TUI flow tests**

Require:

- Stage A preview performs zero writes and never prints keys.
- Stage A Apply requires `--apply-plan <id> --yes` or TUI `y` plus Enter.
- Stage B cannot preview until Stage A state is complete.
- Stage B uses a different exact Plan ID and fresh confirmation.
- TUI labels legacy memory as retained but inactive, not migrated.
- `panel open` never prints the Owner key.
- an explicit copy-key action is separate from open and warns about clipboard retention.

- [ ] **Step 2: Run CLI/TUI tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/cli ./internal/tui -run 'Test(Panel|ControlPlane)' -count=1
```

Expected: commands and TUI stages missing.

- [ ] **Step 3: Implement commands and guided stages**

Add exact command forms:

```text
mlink panel provision --dry-run --json
mlink panel provision --apply-plan <id> --yes
mlink panel cutover --dry-run --json
mlink panel cutover --apply-plan <id> --yes
mlink panel status --json
mlink panel doctor --json
mlink panel open
```

Use the existing bounded secret-input patterns. Never accept user keys in argv.

- [ ] **Step 4: Run CLI/TUI tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/cli ./internal/tui ./cmd/mlink -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/cli/panel.go internal/cli/panel_test.go internal/cli/run.go internal/cli/run_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/tui/view_test.go cmd/mlink/runtime.go
rtk git commit -m "feat: guide panel provisioning and cutover"
```

---

### Task 11: Backup, Restore, Identity Export, and Uninstall

**Files:**
- Modify: `internal/identity/bundle.go`
- Modify: `internal/identity/bundle_test.go`
- Modify: `internal/app/identity.go`
- Modify: `internal/app/identity_test.go`
- Modify: `internal/app/restore.go`
- Modify: `internal/app/restore_test.go`
- Modify: `internal/app/uninstall.go`
- Modify: `internal/app/uninstall_test.go`
- Modify: `internal/cli/identity.go`
- Modify: `internal/cli/identity_test.go`

**Interfaces:**
- Extends identity bundle to carry generated non-secret IDs and dynamic mappings.
- Preserves secrets only through encrypted bundle fields or Keychain references.

- [ ] **Step 1: Write failing portability and uninstall tests**

Tests cover:

- export/import retains Owner control-plane IDs and dynamic mappings;
- raw Feishu IDs and user keys are absent from decrypted non-secret structures;
- restore reconciles Agent metadata markers before trusting local mappings;
- ordinary uninstall removes Panel container and registry but keeps backend metadata;
- backend metadata deletion requires a separate plan with memory counts;
- no uninstall deletes MemoryCore L0-L3 implicitly.

- [ ] **Step 2: Run identity/app tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity ./internal/app ./internal/cli -run 'Test.*ControlPlane' -count=1
```

Expected: generated IDs and mappings not exported.

- [ ] **Step 3: Implement versioned bundle and lifecycle behavior**

Add a new bundle version containing `ControlPlaneState` and `[]PrincipalAgent`. Reject collisions where the same fingerprint maps to a different Core Agent. Keep backend metadata on default uninstall and mark local mappings inactive.

- [ ] **Step 4: Run identity/app/CLI tests**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/identity ./internal/app ./internal/cli -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/identity/bundle.go internal/identity/bundle_test.go internal/app/identity.go internal/app/identity_test.go internal/app/restore.go internal/app/restore_test.go internal/app/uninstall.go internal/app/uninstall_test.go internal/cli/identity.go internal/cli/identity_test.go
rtk git commit -m "feat: preserve generated memory mappings"
```

---

### Task 12: Offline End-to-End and Adversarial Matrix

**Files:**
- Create: `internal/e2e/control_plane_test.go`
- Create: `internal/e2e/panel_lifecycle_test.go`
- Modify: `internal/e2e/identity_test.go`
- Create: `docs/testing/mlink-control-plane-offline.md`

**Interfaces:**
- Exercises all earlier interfaces with fake Core, fake Docker, fake Keychain, fake Orb, and real Journal SQLite.

- [ ] **Step 1: Add failing end-to-end scenarios**

Cover these exact cases:

```text
Stage A preview zero-write
Stage A create and resume
Stage A partial remote create reconciliation
Stage B exact cutover and rollback
Owner cross-Agent shared L1/L2/L3
two DMs -> two backend Agents
two groups -> two backend Agents
two topics -> same group Agent, different Session
private/group include_agent_shared=false
provision failure -> zero Provider calls
quota failure isolated to new principal
raw-ID secret scan
identity export/import
Panel uninstall retains backend metadata
legacy memory never queried after cutover
```

- [ ] **Step 2: Run E2E tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/e2e -run 'Test(ControlPlane|Panel)' -count=1
```

Expected: failures identify missing integration wiring.

- [ ] **Step 3: Complete only the wiring required by failing scenarios**

Keep adapter code unchanged except where the tests prove a routing dependency. Do not add migration or Knowledge/Proxy support.

- [ ] **Step 4: Run the full offline verification matrix**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./...
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test -shuffle=on -count=2 ./...
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go vet ./...
rtk env -u GOROOT GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 /opt/homebrew/bin/go test ./...
```

Expected: PASS. Record the known local Xcode arm64/arm64e race-test limitation without changing system configuration.

- [ ] **Step 5: Write evidence and commit**

```bash
rtk git add internal/e2e/control_plane_test.go internal/e2e/panel_lifecycle_test.go internal/e2e/identity_test.go docs/testing/mlink-control-plane-offline.md
rtk git commit -m "test: verify generated memory control plane"
```

---

### Task 13: Isolated Live MemoryCore and Panel Acceptance

**Files:**
- Create: `internal/e2e/live_control_plane_test.go`
- Create: `docs/testing/mlink-control-plane-live.md`

**Interfaces:**
- Uses existing isolated `mlink-memorycore-test:v2.0.1` with a unique Service ID.
- Builds the pinned Panel image and runs a uniquely named test Panel container on a non-production test port.

- [ ] **Step 1: Add guarded integration tests**

Require `MLINK_TEST_CONTROL_PLANE=1`, MemoryCore URL/token, unique Service ID, and explicit test Panel port. Tests must refuse `service_id=default`, port 8125, or the real Panel container name.

- [ ] **Step 2: Run live Stage A and dynamic mapping tests**

Use unique IDs such as:

```text
service: mlink-control-e2e-<timestamp>
panel container: mlink-memory-panel-e2e-<timestamp>
host port: dynamically allocated loopback port
```

Verify admin/Owner separation, Team/Agent/Asset creation, Panel login API, Owner layers, private/group Agent creation, topic sessions, restart reconciliation, quota error shape, and no raw identifiers in metadata.

- [ ] **Step 3: Verify no model proxy and no protected config drift**

Assert no listener or container is added for port 8096. Rehash Codex, Pi, and Hermes protected model/auth configuration before and after.

- [ ] **Step 4: Remove only uniquely named live-test resources**

Delete the test Panel container and test metadata created under the unique Service ID. Do not touch Service ID `default`, the active MLink config, or current memory.

- [ ] **Step 5: Record and commit live evidence**

```bash
rtk git add internal/e2e/live_control_plane_test.go docs/testing/mlink-control-plane-live.md
rtk git commit -m "test: validate tencent panel control plane"
```

---

### Task 14: Real-Machine Stage A and Stage B Approval Gates

**Files:**
- Create: `docs/testing/mlink-control-plane-real-preview.md`
- Modify after approval: same file with applied evidence.

**Interfaces:**
- Produces exact real-machine Stage A and Stage B ChangeSets.
- Performs no real mutation before each fresh user confirmation.

- [ ] **Step 1: Build and hash the final fixed candidate**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go build -trimpath -o /tmp/mlink-control-plane-preview ./cmd/mlink
rtk shasum -a 256 /tmp/mlink-control-plane-preview
```

Record the hash and prove the installed binary is unchanged.

- [ ] **Step 2: Generate reproducible Stage A preview**

Supply Gateway Bearer only through bounded stdin/Keychain. Run twice and require the same Plan ID. Show:

```text
pinned Panel source/image
loopback port mapping
metadata resource intents
admin/Owner credential separation
registry secret-file behavior
zero active-routing changes
no 8096/model changes
```

- [ ] **Step 3: Prove Stage A preview is zero-write and stop**

Recheck metadata row counts, Keychain accounts, Docker containers/images, active MLink config, Hermes config, Agent hooks, Journal mappings, and ports. Write the report, commit it, show the exact Stage A ChangeSet, and stop for explicit confirmation.

- [ ] **Step 4: After Stage A confirmation, apply and verify provisioning**

Apply only the confirmed Stage A Plan. Verify Panel health/login, generated IDs, Keychain separation, and unchanged active routing. Update the report with redacted generated-ID suffixes.

- [ ] **Step 5: Generate exact Stage B cutover preview and stop again**

Show concrete generated IDs, legacy no-migration notice, Broker/Hermes restarts, protected hashes, and rollback. Run twice for an identical Plan ID, commit the updated report, and stop for fresh confirmation.

- [ ] **Step 6: After Stage B confirmation, apply and run final acceptance**

Verify Owner Codex/Pi/Hermes behavior, two isolated DMs, two isolated groups, two topics in one group, Panel visibility, restart persistence, no old-scope recall, no raw-ID leakage, clean Journal, healthy MemoryCore, and no port 8096 listener.

- [ ] **Step 7: Commit final real-machine evidence**

```bash
rtk git add docs/testing/mlink-control-plane-real-preview.md
rtk git commit -m "docs: record tencent panel cutover"
```

---

## Final Self-Review Checklist

- Tasks 1–3 establish all schema and persistence types before provisioning or routing uses them.
- Tasks 4–5 isolate Stage A metadata and Panel side effects from active routing.
- Tasks 6–8 make dynamic provisioning fail-closed and keep raw external IDs out of Core metadata.
- Task 9 is the only path that activates generated IDs and explicitly performs no memory migration.
- Task 10 requires separate Stage A and Stage B confirmations in CLI and TUI.
- Task 11 makes generated IDs portable without making backend metadata deletion implicit.
- Tasks 12–13 cover offline adversarial behavior and isolated official Panel/Core integration.
- Task 14 prevents any real metadata or cutover mutation before the exact current Plan is shown and approved.
- Owner runtime recall remains L1/L2/L3; private/group runtime recall remains L1 only.
- Panel may show Core-generated L2/L3 for private/group Agents, but MLink never injects them.
- The existing `personal / keliang-personal / usr_owner_keliang` memory is retained physically and inactive after cutover.
- No task modifies model URLs, subscriptions, API providers, MemoryProxy, Knowledge Service, HyMemory data, or the user's unstaged `README.md`.
