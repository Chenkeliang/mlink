# MLink Provider Backend Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect and install the selected memory backend before first capture, provision permanent Core identity without Panel, and make new installs Schema v3 from their first write.

**Architecture:** A Provider lifecycle registry owns backend detection and official deployment separately from the Provider JSON-RPC connector. TencentDB installs the pinned standalone MemoryCore image, then the existing control-plane service provisions permanent IDs headlessly. Panel becomes an optional runtime over the same saved identity.

**Tech Stack:** Go 1.27.0, Bubble Tea, Docker/OrbStack, macOS Keychain, TencentDB MemoryCore v2.0.1 public image.

**Spec:** `docs/superpowers/specs/2026-09-01-mlink-provider-onboarding-design.md`

## Global Constraints

- No Agent integration or Broker start before active Core-generated Schema v3 identity.
- No MemoryProxy, port 8096, or Agent model/auth mutation.
- Image digest is exactly `sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11`.
- Backend mutations require exact Plan ID and `--yes`.
- Secret values never enter Plan JSON, logs, argv, normal backups, or MLink YAML.
- MemoryCore and Knowledge data volumes survive ordinary uninstall.

---

### Task 1: Provider lifecycle contracts and registry

**Files:**
- Create: `internal/provider/lifecycle/types.go`
- Create: `internal/provider/lifecycle/registry.go`
- Test: `internal/provider/lifecycle/registry_test.go`

**Interfaces:**
- Produces: `BackendState`, `BackendStatus`, `BackendInstallRequest`, `BackendLifecycle`, `Registry.Get`.

- [x] Write failing tests proving exact Provider-ID lookup, duplicate rejection, unavailable Provider failure, secret-redacted request formatting, and immutable status copies.
- [x] Run `rtk go test ./internal/provider/lifecycle -count=1`; expect missing types.
- [x] Implement the interfaces and a small explicit registry with no global mutable singleton.
- [x] Run the focused tests; expect PASS.
- [x] Commit `feat: define provider backend lifecycle`.

### Task 2: TencentDB backend detection

**Files:**
- Create: `internal/provider/tencentdb/deployment.go`
- Create: `internal/provider/tencentdb/deployment_test.go`

**Interfaces:**
- Consumes: `lifecycle.BackendStatus`, `config.Connection`, `install.CommandRunner`.
- Produces: `Deployment.Detect(context.Context, config.Connection)`.

- [x] Write failing HTTP/Docker fixture tests for reachable compatible Core, absent container, stopped owned container, incompatible listener, wrong image, wrong volume, and unreachable remote HTTPS.
- [x] Run `rtk go test ./internal/provider/tencentdb -run TestDeploymentDetect -count=1`; expect missing deployment API.
- [x] Implement loopback/HTTPS validation, bounded `/health`, and read-only Docker inspect classification.
- [x] Run TencentDB tests; expect PASS.
- [x] Commit `feat: detect tencentdb memorycore backend`.

### Task 3: Secret-safe official MemoryCore deployment

**Files:**
- Modify: `internal/provider/tencentdb/deployment.go`
- Modify: `internal/provider/tencentdb/deployment_test.go`
- Create: `internal/provider/tencentdb/templates/tdai-gateway.yaml.tmpl`

**Interfaces:**
- Produces: `Deployment.PlanInstall` and `Deployment.ApplyInstall` satisfying `lifecycle.BackendLifecycle`.

- [x] Write failing tests for the pinned image, loopback port, network, persistent volume, deterministic config, secret-free Plan/argv, temporary env-file deletion, Keychain rollback, health verification, and volume retention.
- [x] Run focused tests; expect missing install methods.
- [x] Implement exact ChangeSet planning and Apply-time protected env materialization without shell command construction.
- [x] Run focused tests plus `rtk go test ./internal/install ./internal/secret -count=1`; expect PASS.
- [x] Commit `feat: install official memorycore backend`.

### Task 4: Decouple control plane from Panel

**Files:**
- Create: `internal/app/control_plane_provision.go`
- Create: `internal/app/control_plane_provision_test.go`
- Modify: `internal/app/panel_control_plane.go`
- Modify: `internal/app/panel_control_plane_test.go`

**Interfaces:**
- Produces: `Service.PlanControlPlaneProvision`, `ApplyControlPlaneProvision`, `PlanPanelRuntime`, `ApplyPanelRuntime`.

- [x] Write failing tests proving headless provisioning works with no Panel runtime, Panel-later reuses identical state, and Panel Plan contains no metadata-create intents.
- [x] Run focused app tests; expect missing methods.
- [x] Move composition ownership while keeping legacy `PlanPanelProvision` as a compatibility wrapper for existing callers during this task.
- [x] Run control-plane and Panel tests; expect PASS.
- [x] Commit `refactor: provision core identity before panel`.

### Task 5: Fresh install renders Schema v3 directly

**Files:**
- Modify: `internal/app/install.go`
- Modify: `internal/app/install_test.go`
- Modify: `internal/config/store.go`
- Modify: `internal/config/store_test.go`

**Interfaces:**
- Consumes: active `journal.ControlPlaneState`.
- Produces: direct Schema v3 `PlanInstall`; legacy Schema v2 remains readable for migration only.

- [x] Write failing tests proving missing state blocks fresh install, active state emits Core IDs and routing policies, no Schema v2 is written, and Broker service action is last.
- [x] Run focused tests; expect current Schema v2 behavior to fail the assertions.
- [x] Implement direct v3 rendering and preserve the old cutover path only when an existing v2 config is detected.
- [x] Run app/config/e2e lifecycle tests; expect PASS.
- [x] Commit `feat: start new installs on core identity`.

### Task 6: CLI backend and control-plane commands

**Files:**
- Create: `internal/cli/provider.go`
- Create: `internal/cli/provider_test.go`
- Create: `internal/cli/control_plane.go`
- Create: `internal/cli/control_plane_test.go`
- Modify: `internal/cli/run.go`
- Modify: `cmd/mlink/runtime.go`

**Interfaces:**
- Adds: `provider status/install` and `control-plane provision` commands from the spec.

- [x] Write failing CLI tests for JSON status, local/existing choices, exact confirmation, secret input, stale Plan, and safe exit codes.
- [x] Run CLI tests; expect unsupported commands.
- [x] Implement command parsing and runtime lifecycle wiring without widening the main `Application` interface.
- [x] Run CLI/cmd tests; expect PASS.
- [x] Commit `feat: manage provider backend from cli`.

### Task 7: Guided TUI onboarding and dynamic navigation

**Files:**
- Modify: `internal/tui/model.go`
- Modify: `internal/tui/model_test.go`
- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go`

**Interfaces:**
- Consumes: backend detection/install and headless control-plane application methods.
- Produces: truthful backend status and installation choice before Agent selection.

- [x] Write failing transition tests for reachable, stopped, absent, install-local, connect-existing, failure, resume, and four-Agent Cursor navigation.
- [x] Run TUI tests; expect the hard-coded flow and cursor bound to fail.
- [x] Add Backend Detect/Mode/Install and Control Plane steps; derive all movement bounds from slice lengths.
- [x] Run TUI tests and render snapshots at 80×24, 100×30, and 140×40; expect aligned output without overflow.
- [x] Commit `feat: guide provider installation in tui`.

### Task 8: Doctor, uninstall, and migration guardrails

**Files:**
- Modify: `cmd/mlink/diagnostics.go`
- Modify: `cmd/mlink/main_test.go`
- Modify: `internal/app/uninstall.go`
- Modify: `internal/app/uninstall_test.go`

**Interfaces:**
- Adds stable checks: `provider.backend`, `provider.image`, `provider.volume`, `control_plane.identity_gate`.

- [x] Write failing tests for absent/incompatible backend remediation, custom-ID write blocking, owned-container removal, unowned-container refusal, and volume retention.
- [x] Implement read-only diagnostics and semantic uninstall resources.
- [x] Run Doctor/app tests; expect PASS.
- [x] Commit `feat: complete provider backend lifecycle`.

### Task 9: Real official-image acceptance

**Files:**
- Create: `internal/e2e/live_provider_onboarding_test.go`
- Create: `docs/testing/mlink-provider-onboarding-live.md`

**Interfaces:**
- Verifies the complete fresh-machine sequence against an isolated container/volume/ports.

- [x] Build a unique isolated test fixture that refuses `default`, production container names, ports 8420/8125/8424, and production volumes.
- [x] Verify absent→install→health→headless control plane→Schema v3 install→capture/recall→Panel-later ID equality→uninstall with volume retained.
- [x] Run full, shuffle, vet, race, secret scan, packaged binary, and adversarial isolation suites.
- [x] Record exact image digest, Plan IDs, generated ID suffixes, and cleanup evidence without secret values.
- [x] Commit `test: accept provider onboarding lifecycle`.
