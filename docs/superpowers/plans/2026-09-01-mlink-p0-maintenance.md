# MLink P0 Maintenance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox syntax.

**Goal:** Ship audited Journal resolution, Hermes grant rotation, versioned atomic upgrade, and actionable Doctor under Go 1.27.

**Architecture:** Maintenance mutations use existing exact ChangeSet/Apply gates. Journal delivery state remains immutable while a separate resolution records operator decisions. Credential and binary changes use protected backups and compensating restarts.

**Tech Stack:** Go 1.27.0, SQLite v5 migration, macOS Keychain, Orb, Docker.

**Spec:** `docs/superpowers/specs/2026-09-01-mlink-p0-maintenance-design.md`

## Constraints

- Preserve active Schema v3 routing and all model/auth configuration.
- Never print payload content or credentials.
- No replay command for ambiguous Capture.
- Every mutation requires exact Plan ID and `--yes`.

### Task 1: Journal resolution repository

**Files:** create `internal/journal/migrations/005_event_resolution.sql`; modify `internal/journal/store.go`, `events.go`, `diagnostics.go`; add tests.

- [ ] Write failing tests for migration, unresolved list, unique suffix >=8, delivered/discarded resolution, payload clearing, double resolution, QueueSummary and blockers.
- [ ] Run `rtk go test ./internal/journal -run 'Test.*Resolution' -count=1`; expect missing APIs.
- [ ] Add `EventResolution`, `ResolveEvent`, `ListUnresolvedEvents`, `ResolveUnresolvedEvent` and migration v5 without changing original `state`.
- [ ] Run `rtk go test ./internal/journal -count=1`; expect PASS.
- [ ] Commit `feat: resolve journal delivery outcomes`.

### Task 2: Journal maintenance CLI

**Files:** create `internal/app/maintenance_journal.go`, tests, `internal/cli/maintenance.go`, tests; modify `internal/cli/run.go`, `cmd/mlink/runtime.go`.

- [ ] Write failing Plan/Apply and CLI tests for list/inspect/discard/acknowledge, required reason/provider-ref, redaction, suffix collision and stale Plan.
- [ ] Run focused app/CLI tests; expect missing commands.
- [ ] Implement read-only descriptors and exact service-intent ChangeSets; Apply revalidates event state/hash before resolving.
- [ ] Run app/CLI/cmd tests; expect PASS.
- [ ] Use the new command to preview and discard the one old event with reason `legacy scope inactive; backend L0 session empty`.
- [ ] Commit `feat: add journal maintenance commands`.

### Task 3: Hermes credential rotation

**Files:** create `internal/app/credentials.go`, tests; extend `internal/cli/maintenance.go`, tests; modify runtime wiring.

- [ ] Write failing tests for preview zero-write, secret-free Plan, Keychain/Orb/restart failures, rollback, new=200/old=401 and stale Plan.
- [ ] Run focused tests; expect missing rotation service.
- [ ] Implement Apply-time randomness, protected writers, exact rollback and fingerprint-only diagnostics.
- [ ] Run focused and full tests.
- [ ] Commit `feat: rotate hermes broker credentials`.

### Task 4: Version and atomic upgrade

**Files:** create `internal/version/info.go`, tests, `internal/app/upgrade.go`, tests, `internal/cli/version.go`, tests; extend maintenance CLI and main runtime.

- [ ] Write failing tests for `version --json`, candidate validation, schema compatibility, hash Plan, atomic replace/restart/rollback.
- [ ] Run tests; expect missing APIs.
- [ ] Implement ldflags-backed `Version`, `Commit`, `BuildDate`, schema min/max; implement exact upgrade.
- [ ] Run tests and build a versioned candidate under Go 1.27.
- [ ] Commit `feat: add versioned atomic upgrades`.

### Task 5: Doctor and preflight

**Files:** modify `internal/doctor/check.go`, tests, `cmd/mlink/diagnostics.go`, tests/docs.

- [ ] Write failing checks for binary/schema, Docker/Orb/Keychain/ports/arch/disk/network, Hub digest/args/volume, grant fingerprint, unresolved remediation.
- [ ] Implement read-only checks with stable codes/messages.
- [ ] Run full, shuffle, vet and real Doctor.
- [ ] Upgrade the installed binary through the new command and verify Doctor clean.
- [ ] Commit `feat: complete maintenance doctor`.
