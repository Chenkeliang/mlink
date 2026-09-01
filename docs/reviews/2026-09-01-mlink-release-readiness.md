# MLink v0.1 Release Readiness

Date: 2026-09-01

Target: macOS arm64, Go 1.27.x, TencentDB MemoryCore v2.0.1, official Memory Hub image pinned by digest.

## Verdict

MLink fully covers the first-release **local memory lifecycle** for Codex, Cursor Desktop/CLI, Pi, and Hermes Agent. It does not yet cover every future Knowledge, migration, provider, platform, or fleet-management scenario.

## Verified memory chain

| Boundary | Evidence | Result |
|---|---|---|
| Agent adapter → Broker | Codex, Cursor, Pi, Hermes Journal rows; local health probes | Passed |
| Broker authorization | Codex/Pi/Cursor fixed Owner grants; Hermes delegated grant | Passed |
| Journal → Provider Host | accepted delivery rows; unresolved list empty | Passed |
| Provider → MemoryCore | Core health 200; capture accepted; recall complete | Passed |
| L1 / L2 / L3 | one Owner query returned L1=3, L2=1, L3=1 | Passed |
| Owner continuity | Feishu alias rotation live acceptance | Passed |
| DM isolation | other principal cannot recall Owner canary | Passed |
| Group/topic isolation | same group topics share group L1; other group/Owner isolated | Passed |
| Hub / Panel / Knowledge | Panel, Knowledge, instance visibility, image and volume checks | Passed |
| Cursor MCP | official Cursor CLI discovered both tools; strict Broker errors | Passed |

## Verified lifecycle coverage

| Capability | Status |
|---|---|
| Guided TUI first install with Agent/Provider/identity selection | Complete |
| Exact Plan/Apply and protected model/auth invariants | Complete |
| Incremental Cursor Hook + MCP enablement | Complete |
| Dependency, architecture, disk, network, Hub and secret Doctor | Complete |
| Config drift without stale/service false positives | Complete |
| Automatic file backup and exact restore | Complete |
| Encrypted identity export/import | Complete |
| Audited Journal list/inspect/discard/acknowledge | Complete |
| Hermes grant rotation with rollback and old-token rejection | Complete |
| Version metadata and atomic binary upgrade/rollback | Complete |
| Four-Agent semantic uninstall after Cursor enable and grant rotation | Complete |
| Ordinary uninstall retains MemoryCore data and Knowledge volume | Complete |
| GitHub Release build, checksums and provenance workflow | Implemented; remote run pending |
| npm/npx checksum-verifying thin bootstrap | Implemented; npm authentication/scope pending |

## Fresh verification

- `go test ./... -count=1`: 532 passed.
- `go test ./... -shuffle=on -count=1`: 532 passed.
- Core race suite: 266 passed across Journal, Broker, control plane, Provider, app, and adapters.
- `go vet ./...`: passed.
- npm bootstrap: 4 passed.
- Real Doctor: 29/29 passed.
- Real drift: 11/11 matching.
- Real unresolved Journal events: 0.
- Full uninstall preview: 14 operations; Cursor resources 2; binary 1; Hub container 1; Knowledge volume removal 0.

## Intentionally outside v0.1

- Cursor Cloud Agent and Tab memory;
- Claude Code, OpenCode, OpenClaw, Windows, and Linux desktop lifecycle;
- a second real Provider such as mem0;
- automatic HyMemory migration or backend L0-L3 export/import;
- explicit MemoryCore/Knowledge-volume backup and cross-machine restore;
- local folder, private GitLab, and scheduled Knowledge synchronization;
- destructive backend/volume purge;
- fleet enrollment, device revocation, central policy distribution;
- `doctor --fix`, support-bundle/log retention, and wizard resume after process exit.

These omissions do not break the supported local memory path; they remain separate, explicit future capabilities rather than hidden compatibility behavior.
