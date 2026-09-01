# MLink Provider Backend Onboarding Design

Date: 2026-09-01

Status: approved for implementation

## Purpose

Make Provider selection truthful and safe when MLink is installed before its memory backend. A new installation must detect whether the selected backend exists, offer an official local installation or an existing endpoint, establish permanent Core-generated identity before the first capture, and keep Memory Hub optional.

## Non-negotiable identity invariant

No Agent adapter, Broker, Hook, MCP server, Extension, or Hermes MemoryProvider may become active until the selected backend and its permanent control-plane identity are active.

For TencentDB, the first writable MLink configuration is Schema v3 and contains Core-generated:

- system administrator and credential;
- normal Owner user and credential;
- Owner Team;
- Owner Agent;
- Owner Chat Memory Asset.

Panel is not an identity generator. It is an optional UI over the same MemoryCore metadata. Installing Panel later must reuse the existing control-plane state byte-for-byte and must never mint replacement Owner, Team, Agent, or Asset IDs.

Schema v2 and the existing v2→v3 cutover remain supported only for installations created before this onboarding flow. They are not used for new installations.

Installing the MemoryCore runtime alone is not an initialized MLink memory plane. In that intermediate state MLink reports `pending_identity`, installs no Agent integration, and accepts no capture. MLink must never substitute a user-chosen `agent_id` for the Core metadata object: the headless control-plane step calls the same official MemoryCore metadata APIs that the Hub uses, records the returned IDs, and makes those IDs authoritative. A Hub installed later receives the existing instance and Owner credential and therefore opens the same Team, Agent, and Asset instead of bootstrapping replacements. Any memory written outside MLink under an arbitrary pre-provisioning `agent_id` remains a separate legacy scope and is not silently remapped.

## Product flow

The guided TUI becomes:

```text
Welcome
→ Local Agent detection
→ Provider selection
→ Backend detection
   ├─ reachable and compatible → Use existing backend
   ├─ local container stopped → Start existing local backend
   ├─ absent → Install official local backend
   └─ remote endpoint → Verify endpoint
→ Connection and protected credentials
→ Owner Feishu binding
→ Dynamic Agent capacity
→ Backend install preview/apply when selected
→ Core control-plane preview/apply
→ Agent selection
→ Schema v3 MLink preview/apply
→ Verification
→ Optional Memory Hub preview/apply
→ Complete
```

Provider and deployment-mode choices use normal TUI navigation. Movement bounds are derived from the actual option count; no hard-coded last index is allowed. This fixes the current inability to move to the fourth Cursor option.

## Provider lifecycle boundary

MLink adds a local backend lifecycle registry independent of the Provider JSON-RPC protocol:

```go
type BackendState string

const (
    BackendReachable       BackendState = "reachable"
    BackendStopped         BackendState = "stopped"
    BackendAbsent          BackendState = "absent"
    BackendIncompatible    BackendState = "incompatible"
    BackendRemoteUnreachable BackendState = "remote_unreachable"
)

type BackendStatus struct {
    ProviderID string
    State BackendState
    Endpoint string
    Version string
    Local bool
    Installed bool
}

type BackendInstallRequest struct {
    ProviderID string
    Endpoint string
    GatewayToken []byte
    LLMBaseURL string
    LLMModel string
    LLMAPIKey []byte
}

type BackendLifecycle interface {
    Detect(context.Context, config.Connection) (BackendStatus, error)
    PlanInstall(context.Context, BackendInstallRequest) (install.ChangeSet, error)
    ApplyInstall(context.Context, string, BackendInstallRequest) error
}
```

The registry is keyed by Provider ID. A future mem0 connector can register its own detector and official installer without changing Codex, Cursor, Pi, Hermes, the Broker memory model, or TencentDB code. Selecting an unimplemented Provider must show `connector unavailable`; MLink must not pretend to install mem0 before a real connector exists.

## TencentDB official local deployment

The TencentDB lifecycle follows the official standalone `memory-core` deployment, not the all-in-one Proxy path:

- official multi-architecture image: `agentmemory/memory-core@sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11`;
- container: `tdai-memory-core`;
- network: `tdai-memory-stack`;
- persistent volume: `tdai-memory-core-data`;
- host binding: `127.0.0.1:8420:8420`;
- generated non-secret gateway config under `~/.mlink/memorycore/tdai-gateway.yaml`;
- no MemoryProxy and no port 8096;
- no Agent model URL, subscription, API key, or authentication change.

The memory LLM key and gateway token enter through protected input and are absent from Plan JSON, logs, argv, normal configuration, and backups. Apply uses a private temporary Docker env file, starts the container, verifies `/health`, then deletes the temporary file. The Keychain remains the source of truth. Docker retains runtime environment values as part of container state; Doctor reports this local-runtime boundary explicitly. The container is loopback-bound and must not be treated as a network secret store.

The Plan pins the image digest, ports, volume, network, config hash, rollback commands, and protected secret transport invariant. Failure removes the newly created container and network when owned, but retains the data volume unless a separately confirmed purge is requested.

## Detection rules

TencentDB detection is read-only:

1. Parse and validate the configured endpoint.
2. Call `GET /health` with a bounded timeout.
3. For `127.0.0.1:8420`, inspect `tdai-memory-core` when Docker is available.
4. Classify image digest, port binding, volume, and health independently.
5. Treat another process listening on 8420 as `reachable` only when the MemoryCore health envelope is compatible.
6. Never install over an incompatible listener or an unowned container.

## Headless control-plane provisioning

Control-plane provisioning is separated from Panel runtime provisioning:

```text
PlanControlPlaneProvision / ApplyControlPlaneProvision
PlanPanelRuntime / ApplyPanelRuntime
```

The headless control-plane step uses the existing idempotent metadata markers and Journal state. It is safe to resume after interruption. Once provisioned, new-install rendering consumes the saved state and produces Schema v3 directly. Broker start is the final mutation, after config verification.

Panel installation later only:

- renders the protected instance registry from the existing state;
- installs/reuses the pinned official Memory Hub image and Knowledge volume;
- verifies Panel, Knowledge, and existing instance visibility;
- refuses any Plan that changes Core identity fields.

## CLI

Add read-only status and exact mutation commands:

```text
mlink provider status [--json]
mlink provider install tencentdb --dry-run --json
mlink provider install tencentdb --apply-plan <id> --yes
mlink control-plane provision --dynamic-agent-limit <n> --dry-run --json
mlink control-plane provision --dynamic-agent-limit <n> --apply-plan <id> --yes
```

`mlink install` for a fresh installation refuses to plan when permanent control-plane state is absent. Existing Schema v2 installations receive the legacy cutover remediation instead of silently creating new memory scope.

## Security and failure handling

- Provider detection is always read-only.
- Local install requires an exact Plan and `--yes`.
- Secrets are generated or accepted only after deployment mode is chosen.
- Plan rendering contains fingerprints and presence markers only.
- No shell command is assembled from user input.
- Local endpoint must be loopback; remote endpoint must be explicit HTTPS unless the host is loopback.
- Agent integration is not installed on backend or control-plane failure.
- Re-running every successful stage is idempotent.
- Ordinary uninstall removes owned container/config state but retains MemoryCore and Knowledge data volumes.

## Verification

- exact Detector state tests for reachable, stopped, absent, incompatible, and remote-unreachable;
- secret-free deterministic Plan tests;
- injected pull, config, container, health, Keychain, and rollback failures;
- proof that fresh install writes Schema v3 before the Broker can start;
- proof that Panel-later reuses identical Owner/Team/Agent/Asset IDs;
- proof that no request reaches MemoryCore under custom pre-control-plane IDs;
- dynamic TUI navigation tests including Cursor;
- full, shuffle, vet, race, packaged binary, and isolated official-image acceptance.
