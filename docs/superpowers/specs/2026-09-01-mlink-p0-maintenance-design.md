# MLink P0 Maintenance Release Design

Date: 2026-09-01

Status: approved for implementation

## Purpose

Close the operational gaps discovered during the real Memory Hub rollout before public distribution: unresolved Journal events, credential rotation, binary/config version safety, and actionable Doctor output.

## Scope

### Journal maintenance

Add:

```text
mlink maintenance journal list [--json]
mlink maintenance journal inspect <event-id-or-unique-suffix> [--json]
mlink maintenance journal discard <event-id-or-unique-suffix> --dry-run --json
mlink maintenance journal discard <event-id-or-unique-suffix> --apply-plan <id> --yes
mlink maintenance journal acknowledge <event-id-or-unique-suffix> --provider-ref <ref> --dry-run --json
mlink maintenance journal acknowledge <event-id-or-unique-suffix> --provider-ref <ref> --apply-plan <id> --yes
```

Rules:

- list/inspect never print message content by default;
- inspect returns adapter, route suffixes, timestamps, attempt/error, message count, content hashes, and payload byte count;
- `discard` is allowed only for `ambiguous` or `permanent_failed`, clears payload, records `resolved_discarded`, operator timestamp, and audit reason;
- `acknowledge` requires an explicit provider reference and records `resolved_delivered`; it never fabricates automatic proof;
- no automatic replay command is provided for non-replay-safe Capture;
- every mutation uses an exact secret-independent Plan and fresh confirmation.

The current pre-cutover Codex ambiguous event may be discarded after preview because its old MemoryCore Session has zero L0 records and the old scope is inactive.

### Credential rotation

Add:

```text
mlink maintenance credentials rotate hermes-grant --dry-run --json
mlink maintenance credentials rotate hermes-grant --apply-plan <id> --yes
```

The Apply path generates a random 32-byte base64url grant only after Plan confirmation, stores it in Keychain, atomically updates Orb `~/.hermes/mlink.json`, restarts Broker and Hermes, verifies new=200 and old=401, and restores both values on failure. Secrets never enter argv, Plan JSON, logs, or ordinary backups.

Gateway/admin/Owner key rotation is reported as unsupported with explicit guidance because the official Core lifecycle/revocation contracts require separate designs; do not implement unsafe local-only replacement.

### Version and atomic upgrade

Add build-injected version metadata:

```text
mlink version [--json]
mlink maintenance upgrade --candidate <absolute-path> --dry-run --json
mlink maintenance upgrade --candidate <absolute-path> --apply-plan <id> --yes
```

The upgrade Plan records current/candidate SHA-256, binary format, platform, configuration schema range, and service restarts. It backs up the binary, atomically replaces it, runs `version --json` from the installed path, restarts Broker, and rolls back on failure. Candidate paths must be absolute regular files, non-symlinks, owned by the current user, and mode-safe. Public releases later supply signature verification before this local upgrade layer.

### Doctor

Doctor adds:

- installed binary version/schema support;
- active config schema compatibility;
- Memory Hub image digest and container-argument drift;
- Knowledge volume presence;
- Hermes grant fingerprint equality without printing either value;
- unresolved Journal event count and remediation command;
- dependency preflight for Docker, Orb, Keychain, ports, architecture, disk, and network.

Checks remain read-only. There is no `doctor --fix` in P0.

## Persistence

Journal migration v5 adds `resolved_state`, `resolved_reason`, `resolved_at`, and `provider_ref` columns to `journal_events`. Existing rows remain unchanged. Terminal resolved rows retain metadata but have `payload=NULL`.

## Security

- Resolve event IDs by full ID or unique suffix; ambiguous suffixes fail closed.
- Plan/render output contains hashes and suffixes only.
- Credential rotation uses protected stdin/PTY writers and Orb atomic writes.
- Never expose model credentials or modify model configuration.
- Maintenance mutations are locally authorized by explicit exact Plan confirmation.

## Verification

- red/green unit tests for every state transition and stale Plan;
- injected failures at Keychain write, Orb write, Broker restart, Hermes restart, and verification;
- old Hermes grant rejected after success and restored after failure;
- upgrade rollback restores byte-identical binary;
- real current ambiguous event resolved only through the new command;
- full test, shuffle, fixed Go toolchain, vet, and real Doctor acceptance.

