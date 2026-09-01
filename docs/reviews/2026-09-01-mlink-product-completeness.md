# MLink Product Completeness Review

Date: 2026-09-01

Scope: complete local-user lifecycle for installation, dependencies, guidance, operation, maintenance, security, import/export, recovery, uninstall, extensibility, and distribution.

## Executive assessment

MLink's core memory path is production-capable for the current single-owner machine and the first supported adapters: Codex, Pi, and Hermes. Core-generated identity routing, private/group isolation, rollback, official Memory Hub, Keychain storage, encrypted identity portability, and adversarial tests are implemented.

MLink is not yet a complete distributable product. The largest remaining gaps are maintenance commands, upgrade/version management, dependency preflight, user documentation/help, full backup coverage, additional Agent adapters, and secure local Knowledge/GitLab synchronization.

Current real-machine evidence:

- Schema v3 control plane is `active`;
- Panel, Knowledge, instance visibility, Broker, provider, Codex, Pi, and Hermes checks pass;
- Owner/Team/Agent IDs are Core-generated;
- no MemoryProxy or port 8096;
- Wiki and CodeGraph assets are empty by design;
- one pre-cutover Codex Journal event remains ambiguous;
- Hermes grant was rotated successfully: new grant HTTP 200, old grant HTTP 401.

## Capability matrix

| Area | Status | Evidence | Missing / risk |
|---|---|---|---|
| Core memory capture/recall | Complete | Provider contract/live tests; real Recall 200 | New-scope content still needs normal post-cutover turns to populate L0-L3 |
| Owner L1/L2/L3 | Complete | Schema v3 routing policy and provider tests | Legacy memory intentionally not migrated |
| Feishu DM isolation | Complete | Dynamic Agent provisioner and adversarial tests | Needs management UI/CLI for mapping inventory and quota |
| Group/topic isolation | Complete | Group fingerprint + topic Session tests | Needs live real-group acceptance after new messages |
| Official Hub | Complete | Digest-pinned official image; Panel/Knowledge health | Knowledge assets are opt-in; no private GitLab connector |
| TUI guided install | Substantial | 15-stage install/Panel/cutover flow | No resume-from-step command or saved wizard state after process exit |
| Noninteractive install | Partial | Exact Plan/Apply commands exist | No single cohesive `install --full` orchestration or lifecycle summary |
| Dependency preflight | Partial | Fail-closed checks occur during operations | No upfront matrix for Docker, Orb, Hermes version, ports, Keychain access, disk, architecture, network, or image availability |
| Status/Doctor | Substantial | Agent, Broker, provider, Hub, identity, queue checks | Installed binary has no self-version comparison; no remediation hints or `doctor --fix` |
| Config drift | Complete for owned files | `config diff`, ownership hashes | No desired-vs-runtime diff for remote Core metadata/Hub container arguments |
| Installation rollback | Complete | Exact backups and transaction tests | Remote Stage A metadata is retained rather than compensating deleted by default |
| Backup restore | Partial | Automatic file backups, list, exact restore | No explicit backup-create command; no Hub Knowledge volume or MemoryCore data backup |
| Identity export/import | Complete for v2 bundle | Encrypted HMAC key, bindings, Core IDs, mappings, admin/Owner keys | No rotation/re-encryption command or bundle inventory/inspect command |
| Skill/session import | External/partial | Tencent `agents/asset-import.ts` exists | Not integrated into MLink CLI/TUI; no progress/status/rollback |
| Local documents | Missing | Official Wiki API available | No folder scanner, hash journal, incremental upload, delete propagation, or schedule |
| Local/company GitLab | Missing | Official CodeGraph supports public HTTPS | No secure private GitLab auth, internal CA policy, or local repository connector |
| Credential security | Strong but incomplete | Keychain, redaction, separate keys, clipboard warning | No product command to rotate Hermes grant, Gateway token, admin key, or Owner key |
| Journal maintenance | Missing | Queue diagnostics and payload retention exist | No list/show/ack/discard/replay command for ambiguous/permanent events |
| Upgrade/downgrade | Missing | Stage B safely updated the binary once | No `version`, compatibility check, signed update, atomic upgrade, or downgrade command |
| Help and usage docs | Missing | Design/test documents exist | `mlink --help` and `mlink version` are unsupported; README still says design in progress |
| Logs/observability | Partial | Local logs, Doctor, Hub logs | No `mlink logs`, event trace, metrics, alerting, or retention controls |
| Uninstall | Substantial | Local resources removed; backend/volume retained safely | No separately confirmed purge plan for Knowledge volume or Core metadata/memory |
| Packaging/distribution | Missing | Local Go binary works | No Homebrew formula, release artifacts, checksums/signing, installer, or update channel |
| Agent coverage | Partial | Codex, Pi, Hermes | Cursor, Claude Code, OpenCode, OpenClaw, and generic MCP/Hook adapters absent |
| Provider plugability | Architectural only | Provider manifest/host boundary exists | Only TencentDB connector is real; mem0 and other providers are not implemented |
| Multi-machine portability | Partial | Encrypted identity bundle | No guided restore of Hub volume/Core data or device enrollment/revocation |
| Security audit | Strong baseline | No model changes, no secret argv in planned paths, raw-ID tests | No formal threat model report, secret inventory command, or automated credential expiry |
| Test coverage | Strong | Unit, shuffle, fixed toolchain, isolated live, real acceptance | Known local race-test blocker remains; no release-install test from packaged artifact |

## Priority backlog

### P0 — operational correctness

1. **Journal maintenance command**
   - `mlink maintenance journal list --json`
   - `mlink maintenance journal inspect <event-suffix>` with content redacted by default
   - exact Plan/Apply for `acknowledge-delivered` and `discard-undelivered`
   - no blind replay of non-replay-safe Capture
   - resolve the current pre-cutover event only after an operator decision

2. **Credential rotation workflows**
   - Hermes Broker grant rotation with Keychain/Orb atomic rollback
   - Gateway Bearer rotation with provider/Hub registry restart
   - admin/Owner user-key replacement and revocation
   - status must compare only secret fingerprints, never print values

3. **Versioned atomic upgrade**
   - `mlink version`
   - installed/candidate config-schema compatibility check
   - exact binary update Plan with rollback and service restart
   - prevent a new config from being applied by an old installed binary

4. **Accurate operational Doctor**
   - ship the Schema v3 routing-policy diagnostics already added in source
   - add Hub container-argument/digest drift and Knowledge volume checks
   - attach remediation codes without mutating automatically

### P1 — complete user lifecycle

5. **Dependency preflight and guided remediation** for Docker/Orb/Hermes/ports/Keychain/network/disk/architecture.
6. **Help, examples, and runbook**: `--help`, per-command help, README, first-run/normal-use/recovery/uninstall guides.
7. **Unified lifecycle/resume**: persist wizard phase and safely resume install, Stage A, or Stage B after interruption.
8. **Explicit backup command** covering MLink state, encrypted identity bundle, Hub Knowledge volume, and documented MemoryCore backup boundary.
9. **Secure local Knowledge Sync** for allowlisted document folders and local Git worktrees; separate private GitLab connector with Keychain credentials and company CA support.
10. **Agent expansion**: Cursor first, then Claude Code and a generic MCP/Hook adapter contract.
11. **Mapping administration**: list/reconcile/deactivate dynamic Agents, capacity usage, orphan marker detection, and suffix-only diagnostics.
12. **Destructive purge workflows** separated from uninstall, with counts and fresh confirmation for Hub volume, metadata, and memory layers.

### P2 — distribution and operations

13. Homebrew and signed GitHub release artifacts; optional npm/npx bootstrap wrapper only if it adds value beyond the native binary.
14. Release update channel, checksums/signatures, rollback channel, and packaged-artifact acceptance tests.
15. `mlink logs`, structured event trace, metrics, alerting, retention, and support bundle with automatic redaction.
16. Additional memory providers such as mem0 through the existing provider boundary.
17. Multi-machine enrollment, device/key revocation, and guided restore.

## Immediate closeout status

- Hermes grant rotation: complete.
- Stage A and Stage B: complete.
- Core/Hub/Broker/Hermes: healthy.
- Codex/Pi/Hermes routing: active; all three Doctor adapter checks pass in the current source candidate.
- Current ambiguous event: backend L0 for its old session is empty; no replay is recommended because the legacy scope is inactive. Product lacks a supported audited discard action, so the database was not edited manually.
- Cursor: not configured because no adapter exists yet.

## Recommended next implementation slice

Implement P0 items 1–4 as one maintenance release before adding more adapters or Knowledge ingestion. That release closes the exact operational gaps uncovered during this installation without expanding the memory model.
