# Official Memory Hub Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace MLink's source-built Panel-only runtime with the digest-pinned official `agentmemory/memory-hub` image, connect its Panel and Knowledge Service to the existing MemoryCore, and apply only Stage A after exact preview and live verification.

**Architecture:** The existing `internal/panel` package remains the protected control-plane runtime boundary but manages the official combined Hub container. It renders the official instance registry during Apply, pulls and verifies the arm64 image digest, creates a persistent Knowledge volume, runs Panel and Knowledge on loopback-only ports, and verifies both services. Existing Core-generated identity provisioning remains unchanged; Stage B routing cutover is explicitly outside the real-machine apply in this plan.

**Tech Stack:** Go 1.22, Docker/OrbStack, official `agentmemory/memory-hub`, TencentDB MemoryCore v3 metadata API, SQLite Journal, macOS Keychain, Bubble Tea TUI.

**Spec:** `docs/superpowers/specs/2026-08-31-mlink-tencent-panel-control-plane-design.md`

## Global Constraints

- Use only official image `agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104` on this arm64 host.
- Do not build or modify TencentDB source or Dockerfiles.
- Do not run MemoryProxy or add a listener on port 8096.
- Do not set `REMOTE_INSTANCE_PROXY_URL` or a registry `proxy_endpoint`.
- Do not modify Codex, Pi, or Hermes model providers, endpoints, subscriptions, or authentication.
- Bind Panel and Knowledge only to `127.0.0.1` on ports `8125` and `8424`.
- Use `LLM_MODE=proxy` only for Knowledge's internal calls to the existing MemoryCore Gateway at `host.docker.internal:8420`.
- Do not ingest or bind Knowledge assets in this plan.
- Keep `tdai-panel-data` on ordinary uninstall; purge requires a separate destructive plan.
- Stage A may create Core metadata and local Hub resources only after exact Plan confirmation. Do not perform Stage B in this plan.
- Preserve the user's unstaged `README.md` and never stage it.
- Every repository shell command begins with `rtk`; Go commands use `rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go ...`.

---

### Task 1: Official Memory Hub Runtime Contract

**Files:**
- Modify: `internal/panel/runtime.go`
- Modify: `internal/panel/runtime_test.go`
- Modify: `internal/panel/registry.go`
- Modify: `internal/panel/registry_test.go`

**Interfaces:**
- Produces: digest-pinned Hub constants, `panel.Desired`, `panel.Status`, `Runtime.Plan`, `Runtime.Apply`, `Runtime.Status`, and `Runtime.OpenURL`.
- Consumes: existing `install.Target`, `install.CommandRunner`, and protected Gateway Bearer supplied only during Apply.

- [ ] **Step 1: Write failing Hub plan tests**

Replace the Panel-only expectations with:

```go
const (
    ContainerName = "tdai-memory-hub"
    ImageReference = "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104"
    VolumeName = "tdai-panel-data"
)

type Desired struct {
    RegistryPath string
    HostAddress string
    PanelHostPort int
    KnowledgeHostPort int
    InstanceID string
    InstanceName string
    GatewayEndpoint string
    KnowledgePublicBaseURL string
    KnowledgeLLMProxyBaseURL string
}
```

Require four operations in this order: protected registry intent, exact `docker pull`, labeled `docker volume create`, and exact `docker run`. Assert the run command contains:

```text
--name tdai-memory-hub
--restart unless-stopped
--add-host host.docker.internal:host-gateway
-p 127.0.0.1:8125:8125
-p 127.0.0.1:8424:8424
-v tdai-panel-data:/data/knowledge
-v <registry>:/app/panel/config/metadata-instances.json:ro
-e KNOWLEDGE_PUBLIC_BASE_URL=http://host.docker.internal:8424/v3
-e KNOWLEDGE_LLM_PROXY_BASE_URL=http://host.docker.internal:8420
-e LLM_MODE=proxy
-e KNOWLEDGE_LLM_BINDING_SYNC=true
```

Also assert absence of `8096`, `REMOTE_INSTANCE_PROXY_URL`, `proxy_endpoint`, Agent model variables, and every credential value.

- [ ] **Step 2: Run the focused tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/panel -run 'Test(Plan|Apply|Status|RenderRegistry)' -count=1
```

Expected: old source-build constants and command expectations fail.

- [ ] **Step 3: Implement the minimal Hub runtime**

`Runtime.Apply` must:

1. recompute and verify the exact Plan ID;
2. render the registry to a protected in-memory buffer;
3. write it atomically at mode `0600` and restore it on failure;
4. run `docker pull`;
5. inspect the pulled RepoDigest and require the exact official digest;
6. create the labeled Knowledge volume;
7. run the Hub container with the exact loopback and environment contract.

Do not accept an unpinned tag or a different RepoDigest. Keep the Gateway Bearer out of argv, Plan JSON, logs, and backups.

- [ ] **Step 4: Implement dual-service health**

Define:

```go
type Status struct {
    ContainerPresent bool `json:"container_present"`
    PanelHealthy bool `json:"panel_healthy"`
    KnowledgeHealthy bool `json:"knowledge_healthy"`
    InstanceVisible bool `json:"instance_visible"`
}
```

Health requires:

- `docker inspect tdai-memory-hub`;
- `GET http://127.0.0.1:8125/health`;
- `GET http://127.0.0.1:8125/api/v1/meta/instances` containing the exact instance ID;
- `GET http://127.0.0.1:8424/health`.

- [ ] **Step 5: Run Panel tests and commit**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/panel -count=1
rtk git add internal/panel/runtime.go internal/panel/runtime_test.go internal/panel/registry.go internal/panel/registry_test.go
rtk git commit -m "feat: run official memory hub"
```

---

### Task 2: Control-Plane, Runtime, and Uninstall Wiring

**Files:**
- Modify: `internal/controlplane/types.go`
- Modify: `internal/controlplane/service.go`
- Modify: `internal/controlplane/service_test.go`
- Modify: `internal/app/panel_control_plane.go`
- Modify: `internal/app/panel_control_plane_test.go`
- Modify: `internal/app/uninstall.go`
- Modify: `internal/app/uninstall_test.go`
- Modify: `cmd/mlink/runtime.go`
- Modify: `cmd/mlink/main_test.go`

**Interfaces:**
- Consumes: Task 1 Hub constants and runtime.
- Produces: Stage A combined ChangeSet and default uninstall behavior for Hub.

- [ ] **Step 1: Write failing integration tests**

Tests must prove:

- control-plane Journal state records container `tdai-memory-hub` and the exact official image digest;
- Stage A preview composes four metadata intents plus four Hub operations and performs zero writes;
- runtime no longer inspects or requires a local `MemoryPanel` source checkout;
- ordinary uninstall removes the Hub container and protected registry;
- ordinary uninstall does not remove `tdai-panel-data`, Core metadata, or L0-L3;
- uninstall rollback recreates the exact Hub container after restoring the registry.

- [ ] **Step 2: Run focused tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane ./internal/app ./cmd/mlink -run 'Test.*(Hub|PanelProvision|ControlPlaneUninstall)' -count=1
```

Expected: old `mlink-memory-panel:a5dcbe6`, source checkout, and `8123` assumptions fail.

- [ ] **Step 3: Update control-plane and runtime wiring**

Set:

```go
PanelContainerName = panel.ContainerName
PanelImageName = panel.ImageReference
```

Remove `SourceRoot` and `verifyPinnedPanelSource` from runtime construction. Build `panel.Desired` exclusively from fixed official endpoints and `~/.mlink/panel/metadata-instances.json`.

Stage A still provisions metadata before starting Hub and remains retry-safe. A Hub startup failure retains the newly created remote metadata as inactive/reconcilable and restores only the protected local registry.

- [ ] **Step 4: Update uninstall without adding a volume purge**

The uninstall plan must use:

```text
docker rm -f tdai-memory-hub
```

Its rollback command must recreate the exact digest-pinned Hub container after the registry backup is restored. Do not run `docker volume rm tdai-panel-data` anywhere in ordinary uninstall.

- [ ] **Step 5: Run integration tests and commit**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/controlplane ./internal/app ./cmd/mlink -count=1
rtk git add internal/controlplane/types.go internal/controlplane/service.go internal/controlplane/service_test.go internal/app/panel_control_plane.go internal/app/panel_control_plane_test.go internal/app/uninstall.go internal/app/uninstall_test.go cmd/mlink/runtime.go cmd/mlink/main_test.go
rtk git commit -m "feat: wire memory hub control plane"
```

---

### Task 3: CLI, TUI, Doctor, and Documentation

**Files:**
- Modify: `internal/cli/panel.go`
- Modify: `internal/cli/panel_test.go`
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`
- Modify: `cmd/mlink/diagnostics.go`
- Modify: `cmd/mlink/diagnostics_test.go`
- Modify: `docs/testing/mlink-control-plane-offline.md`
- Modify: `docs/testing/mlink-control-plane-live.md`

**Interfaces:**
- Consumes: Hub `panel.Status` from Task 1.
- Produces: user-visible Hub/Knowledge status and accurate guided installation copy.

- [ ] **Step 1: Write failing UI and diagnostic tests**

Require status output to distinguish:

```text
hub.container
hub.panel
hub.knowledge
hub.instance
```

TUI Stage A copy must say `Official Memory Hub`, show Panel `8125`, Knowledge `8424`, and state that Knowledge assets are not automatically imported or injected. Narrow layouts must remain overflow-free. No screen may display full generated IDs or user keys.

- [ ] **Step 2: Run focused tests and observe failure**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/cli ./internal/tui ./cmd/mlink -run 'Test.*(Panel|Hub|ControlPlane)' -count=1
```

- [ ] **Step 3: Implement status and copy changes**

`panel doctor` returns pending/failure unless all four Hub status fields pass. `panel open` remains `http://127.0.0.1:8125` and never reads the Owner key. `copy-owner-key --yes` remains the only clipboard action.

- [ ] **Step 4: Update evidence documentation**

Replace all Panel-only build claims with the official digest-pinned Hub contract. Document that Knowledge runs but has no bound assets, and that company GitLab/private local ingestion is not enabled.

- [ ] **Step 5: Run UI tests and commit**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./internal/cli ./internal/tui ./cmd/mlink -count=1
rtk git add internal/cli/panel.go internal/cli/panel_test.go internal/tui/model.go internal/tui/model_test.go internal/tui/view.go internal/tui/view_test.go cmd/mlink/diagnostics.go cmd/mlink/diagnostics_test.go docs/testing/mlink-control-plane-offline.md docs/testing/mlink-control-plane-live.md
rtk git commit -m "feat: report memory hub health"
```

---

### Task 4: Offline, Isolated Live, and Real Stage A Apply

**Files:**
- Modify: `internal/e2e/panel_lifecycle_test.go`
- Modify: `internal/e2e/live_control_plane_test.go`
- Modify: `docs/testing/mlink-control-plane-live.md`
- Modify: `docs/testing/mlink-control-plane-real-preview.md`

**Interfaces:**
- Exercises: official image pull/run, Panel login, Knowledge health, metadata provisioning, cleanup, and real Stage A.
- Produces: applied Stage A with active routing still unchanged.

- [ ] **Step 1: Update guarded live tests**

The live test must refuse:

- Service ID `default`;
- Panel port `8125` or Knowledge port `8424` for isolated testing;
- container name `tdai-memory-hub`;
- an unpinned image reference.

Use a unique container and volume, pull the same official digest, verify Panel instance listing and Owner login, verify Knowledge `/health`, then remove only the unique container and unique test volume. Destroy only the unique Core Service ID.

- [ ] **Step 2: Run the complete offline matrix**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test ./... -count=1
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go test -shuffle=on -count=2 ./...
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go vet ./...
rtk env -u GOROOT GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 /opt/homebrew/bin/go test ./... -count=1
```

- [ ] **Step 3: Run isolated official Hub acceptance**

Use unique values such as:

```text
service: mlink-control-e2e-<timestamp>
container: mlink-memory-hub-e2e-<timestamp>
volume: mlink-memory-hub-e2e-<timestamp>
panel port: non-8125 loopback port
knowledge port: non-8424 loopback port
```

Verify no listener or container is added for port 8096 and rehash Codex, Pi, and Hermes protected model/auth files before and after.

- [ ] **Step 4: Build the real candidate and generate a fresh Stage A preview**

```bash
rtk env -u GOROOT CGO_ENABLED=0 /opt/homebrew/bin/go build -trimpath -o /tmp/mlink-memory-hub-candidate ./cmd/mlink
rtk shasum -a 256 /tmp/mlink-memory-hub-candidate /Users/keliang/.local/bin/mlink
rtk /tmp/mlink-memory-hub-candidate panel provision --dry-run --json
```

Run preview twice and require the same Plan ID. Recheck config/hook/Hermes hashes, Keychain control accounts, Journal schema, containers, images, volumes, and ports to prove zero writes.

- [ ] **Step 5: Apply only the freshly confirmed Stage A Plan**

The user's 2026-09-01 instruction authorizes installation of the official Hub and connection of Panel to MLink. Apply the exact fresh Stage A plan with `--apply-plan <id> --yes`. Do not run `panel cutover`.

Verify:

- official image RepoDigest;
- `tdai-memory-hub` healthy;
- Panel, Knowledge, and instance checks pass;
- Owner login works through the separate clipboard command without printing the key;
- generated admin and Owner credentials differ;
- active `~/.mlink/config.yaml`, Broker, Hermes, Codex, and Pi hashes remain unchanged;
- no port 8096 listener;
- no Knowledge asset is imported or bound.

- [ ] **Step 6: Record evidence and commit**

```bash
rtk git add internal/e2e/panel_lifecycle_test.go internal/e2e/live_control_plane_test.go docs/testing/mlink-control-plane-live.md docs/testing/mlink-control-plane-real-preview.md
rtk git commit -m "test: accept official memory hub"
```

Stage B remains a separate future exact Plan and confirmation.
