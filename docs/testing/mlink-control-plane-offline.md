# MLink TencentDB Control Plane — Offline Verification

Date: 2026-08-31

Scope: Schema v3, Core-generated identities, dynamic private/group Agents, official Panel-only runtime, no-migration cutover, encrypted portability, and local-only uninstall. All tests use fakes or loopback test servers; no production metadata or memory is mutated.

## Matrix

| Requirement | Evidence |
|---|---|
| Stage A preview is zero-write and secret-free | `internal/app/panel_control_plane_test.go`, `internal/controlplane/service_test.go` |
| Stage A create/resume and partial-write reconciliation | `internal/controlplane/service_test.go` |
| Stage B exact cutover and rollback | `internal/app/control_plane_cutover_test.go` |
| Owner L1/L2/L3; private/group L1 only | `internal/broker/http_test.go`, `internal/provider/tencentdb/provider_test.go` |
| Two DMs and two groups isolate backend Agents | `internal/e2e/control_plane_test.go` |
| Same group topics share Agent and split Session | `internal/e2e/control_plane_test.go` |
| Provision/quota failure does not call Provider | `internal/broker/http_test.go`, `internal/controlplane/agents_test.go` |
| Concurrent create, pagination, restart reconciliation | `internal/controlplane/agents_test.go` |
| Raw Feishu IDs absent from Core metadata and authorization | `internal/controlplane/agents_test.go`, `internal/e2e/control_plane_test.go` |
| Encrypted export/import retains IDs, mappings and user keys | `internal/identity/bundle_test.go`, `internal/app/identity_test.go` |
| Imported mappings require marker reconciliation | `internal/controlplane/agents_test.go` |
| Panel is pinned, loopback-only, and has no 8096/model proxy | `internal/panel/runtime_test.go`, `internal/e2e/panel_lifecycle_test.go` |
| Default uninstall removes local Panel only | `internal/app/uninstall_test.go` |
| Legacy memory is never queried or migrated after cutover | `internal/app/control_plane_cutover_test.go` |

## Commands

```text
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go test -shuffle=on -count=2 ./...
CGO_ENABLED=0 go vet ./...
GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 go test ./...
```

The local `-race` run remains excluded because the installed Xcode toolchain reports the known arm64/arm64e linker incompatibility. No system configuration was changed to work around it.
