# MLink Full Backup and Restore Design

Date: 2026-09-01

Status: approved concept; written specification pending user review

## Purpose

Provide a one-command and guided-TUI path for moving an existing MLink workspace to a new computer without changing any MemoryCore User, Team, Agent, Asset, or dynamic-principal mapping ID.

The restore path is distinct from a fresh install. It restores the MemoryCore data plane first, proves that the original identity exists, then restores MLink state and Agent integrations. It never provisions replacement identity over an empty or mismatched backend.

## Scope

The first version targets:

- macOS arm64;
- Go 1.27 MLink binaries;
- the digest-pinned TencentDB MemoryCore standalone image;
- the digest-pinned official Memory Hub image;
- Docker or OrbStack's Docker runtime;
- the local SQLite/file MemoryCore backend;
- the local MemoryKnowledge volume;
- Codex, Cursor, Pi, and Hermes Agent integrations.

The architecture retains a Provider snapshot boundary so a future mem0 connector can implement its own backup driver without changing the TUI, bundle envelope, Keychain restore, or Agent installation stages.

## Non-negotiable identity invariant

A restore must preserve byte-for-byte:

- MemoryCore `service_id` / MLink instance ID;
- Owner User ID;
- Owner Team ID;
- Owner Agent ID;
- Owner Chat Memory Asset ID;
- every dynamic principal/group Agent and Asset mapping;
- the MLink identity HMAC key;
- Feishu binding values and fingerprints.

The restore path must not call `init-admin`, `create-user`, `create-team`, or `create-agent` before the restored Core identity has been inspected. If any expected object is missing or differs, restore stops before Agent Hooks or Broker start.

No automatic ID rewrite is permitted. Re-keying existing memory into a new identity graph is a separate future migration product, not a restore fallback.

## User experience

The TUI welcome screen offers:

```text
Start a new installation
Restore an MLink workspace
Connect an existing MemoryCore
```

Restore flow:

```text
Select encrypted .mlink-backup
→ Enter passphrase
→ Inspect manifest
→ Dependency and capacity preflight
→ Restore preview
→ Exact Plan confirmation
→ Quiesce conflicting local services
→ Create empty formal volumes
→ Restore Core and Knowledge snapshots
→ Start Core in verification mode
→ Verify all fixed and dynamic IDs
→ Verify memory sample hashes
→ Restore MLink config, Journal, and Keychain
→ Install selected Agent integrations
→ Start Hub and Broker
→ Doctor
→ Complete
```

The CLI equivalents are:

```text
mlink backup create --output <absolute-path> --passphrase-stdin --dry-run --json
mlink backup create --output <absolute-path> --passphrase-stdin --apply-plan <id> --yes
mlink backup inspect <bundle> --passphrase-stdin --json
mlink backup restore <bundle> --passphrase-stdin --dry-run --json
mlink backup restore <bundle> --passphrase-stdin --apply-plan <id> --yes
```

The existing automatic installation backups and `mlink identity export/import` remain supported. They are not renamed or presented as full workspace backups.

## Bundle format

The artifact extension is `.mlink-backup`. It is a streaming, authenticated encrypted envelope. The outer header contains only:

- format ID and format version;
- cipher/KDF identifiers and parameters;
- random salt and stream nonce/header;
- encrypted payload length when known.

Everything identifying the user, machine, Core instance, volumes, paths, Agents, or secrets remains inside the encrypted payload.

The encrypted payload is a tar stream containing:

```text
manifest.json
checksums.sha256
core/
  data.tar
knowledge/
  data.tar
mlink/
  config.yaml
  journal.db
  owned-backups.tar
  panel-registry.json
identity/
  identity.bundle
secrets/
  inventory.json
  gateway-token
  memory-llm-key
  hermes-grant
agents/
  selection.json
```

`identity.bundle` keeps the existing encrypted identity schema internally, but the complete outer backup is encrypted again as one authenticated stream. No secret appears in argv, environment dumps, Plan JSON, normal logs, temporary plaintext files, or unencrypted checksums.

The implementation uses streaming encryption so backup size is not limited by RAM. Passphrases must contain at least 12 bytes. Recovery-key support may be added later without changing the inner manifest.

## Manifest

`manifest.json` records:

- MLink version, commit, schema range, OS, architecture;
- backup timestamp and source host fingerprint;
- Provider ID/version and snapshot-driver version;
- MemoryCore and Memory Hub exact image digests;
- Core/Knowledge volume logical byte counts and file counts;
- Core instance ID and every fixed control-plane ID;
- dynamic mapping fingerprints, route kinds, and backend IDs;
- active Agent adapters;
- config schema and Journal migration version;
- per-artifact SHA-256;
- consistency state and shutdown evidence.

It contains no raw Feishu ID, user key, token, LLM key, memory content, or document content outside the encrypted envelope.

## Consistent backup creation

Backup creation is an exact, recoverable mutation workflow even though the resulting operation is primarily read-only:

1. Detect the active Provider and owned/external runtime boundary.
2. Refuse unknown containers, mount paths, image drift, or unsupported storage backends.
3. Verify Journal queue state and MemoryCore pipeline state.
4. Pause Broker delivery and stop Memory Hub.
5. Stop MemoryCore cleanly so SQLite WAL is checkpointed.
6. Mount Core and Knowledge volumes read-only in a pinned helper container.
7. Stream both snapshots directly into the encrypted envelope.
8. Export MLink identity and required Keychain secrets directly into the encrypted stream.
9. Generate checksums, file counts, logical sizes, and SQLite `quick_check` evidence.
10. Restart the original Core, Hub, and Broker even if backup creation fails.
11. Re-run Doctor and remove any temporary helper containers.

For an external compatible Core, MLink requires the operator to explicitly approve the discovered container and volume as the backup source. This approval grants snapshot access only; it does not transfer uninstall ownership.

Backup creation does not remove test instances or alter memory. Cleaning obsolete MemoryCore instances is a separate destructive maintenance Plan.

## Provider snapshot boundary

```go
type SnapshotManifest struct {
    ProviderID       string
    DriverVersion    string
    InstanceID       string
    ImageDigest      string
    Volumes          []VolumeSnapshot
    ControlPlane     journal.ControlPlaneState
    PrincipalAgents  []journal.PrincipalAgent
}

type SnapshotDriver interface {
    Detect(context.Context) (SnapshotSource, error)
    PlanBackup(context.Context, BackupRequest) (install.ChangeSet, error)
    StreamBackup(context.Context, BackupRequest, io.Writer) (SnapshotManifest, error)
    PlanRestore(context.Context, RestoreRequest, SnapshotManifest) (install.ChangeSet, error)
    ApplyRestore(context.Context, RestoreRequest, SnapshotManifest, io.Reader) error
    VerifyRestore(context.Context, SnapshotManifest) error
}
```

The first driver is `dev.mlink.tencentdb`. Driver methods receive secret stores and command runners through dependencies; secret values never enter method formatting or ChangeSets.

## Restore safety

Restore refuses to proceed when:

- the archive authentication tag or an inner checksum fails;
- the bundle requires a newer incompatible MLink/schema version;
- target Core/Knowledge volumes are non-empty;
- production container names or ports are owned by an unrelated process;
- the expected image digest is unavailable or differs;
- the destination lacks sufficient free space plus rollback headroom;
- restored SQLite databases fail `quick_check`;
- Core health is incompatible;
- any fixed or dynamic ID differs from the manifest;
- the restored Owner key cannot verify the restored Owner User;
- a bounded set of memory sample IDs/content hashes differs;
- protected Agent model/auth configuration would change.

Existing target data is never overwritten by default. Replacing a target workspace requires a separate future destructive workflow with its own backup and explicit confirmation.

## Restore order

1. Preflight and reserve formal resource names.
2. Restore Core volume into a newly created empty volume.
3. Restore Knowledge volume into a newly created empty volume.
4. Start Core on a temporary loopback verification port.
5. Verify Gateway credential, Owner credential, fixed IDs, dynamic IDs, and sample memory hashes.
6. Restore MLink config and Journal.
7. Import the identity HMAC, Admin/Owner keys, Gateway token, LLM key, Hermes grant, and Feishu bindings into the destination Keychain.
8. Re-plan Agent semantic installation against destination paths.
9. Stop verification Core and start the final Core on `127.0.0.1:8420`.
10. Start Memory Hub and Broker.
11. Verify Cursor/Codex Hooks, Pi Extension, Hermes provider/bridge/session policy, Panel instance visibility, and Journal queue.

The formal TencentDB resource defaults are:

```text
container: tdai-memory-core
volume:    tdai-memory-core-data
network:   tdai-memory-stack
config:    ~/.mlink/memorycore/tdai-gateway.yaml
```

## Credentials UX

TUI adds a Credentials page showing only presence, role, creation/rotation state, and fingerprints.

Allowed explicit clipboard actions:

```text
Copy Panel Owner user_key
Copy Panel Admin user_key
```

Each action requires a fresh confirmation and warns that the clipboard retains the secret until replaced.

Gateway and Memory LLM keys are not offered as ordinary copy actions. They can be rotated or included inside an encrypted full backup. Raw Feishu IDs, identity HMAC keys, and Hermes grants remain non-displayable.

CLI additions:

```text
mlink credentials status --json
mlink credentials copy panel-owner --yes
mlink credentials copy panel-admin --yes
```

## Failure and rollback

Backup failure never changes the source volumes. Services are restarted from their original definitions.

Restore failure before final cutover removes only resources created by the failed Restore Plan. The encrypted bundle remains untouched. Existing containers, volumes, Keychain items, and Agent configuration are unchanged.

If final service start or Doctor fails, MLink stops the newly restored stack and keeps the volumes for inspection. It does not silently create new Core identity or fall back to an empty install.

## Current-machine formalization

The existing workstation has been migrated without ID changes from historical resources to the formal Core resource names. The old test container/image/volume remain stopped as rollback resources until a separate cleanup confirmation.

Historical non-default test instances inside the Core volume are preserved by the formalization copy. They must not be deleted as part of backup/restore implementation. A future `maintenance provider inventory/prune` command may list and destroy selected non-active instances through official APIs with exact counts and a separate confirmation.

## Verification

Automated coverage must include:

- secret-free deterministic Plan and redacted request formatting;
- archive authentication failure and wrong passphrase;
- truncated/corrupt artifact rejection;
- insufficient-space and non-empty-target refusal;
- SQLite integrity failure;
- fixed Owner ID mismatch;
- dynamic Agent mapping mismatch;
- restored Owner credential verification;
- memory sample hash equality;
- backup failure service restart;
- restore failure removal of only newly created resources;
- no `init-admin` or create metadata calls during restore;
- no model/auth mutation for all four Agents;
- full TUI keyboard/navigation snapshots;
- isolated live backup of an official Core/Hub stack and restore into different names/ports;
- final cutover, capture/recall, ordinary uninstall, and volume-retention acceptance.

## Explicitly outside this version

- selective deletion of historical test instances;
- cross-backend migration or ID rewrite;
- live snapshots without a short write pause;
- cloud object-storage upload;
- incremental/deduplicated backup chains;
- scheduled unattended backups;
- Windows/Linux restore UX;
- fleet enrollment and centralized recovery-key escrow;
- MemoryCore/Knowledge destructive purge.
