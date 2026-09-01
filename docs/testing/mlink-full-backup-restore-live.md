# MLink full backup and restore — live acceptance

Date: 2026-09-01  
Platform: macOS arm64, Go 1.27, OrbStack Docker runtime

## Scope

This acceptance used only official digest-pinned images:

- MemoryCore: `sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11`
- Memory Hub: `sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104`
- snapshot helper: `sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc`

The source formal resources remained:

```text
tdai-memory-core
tdai-memory-core-data
tdai-memory-stack
tdai-memory-hub
tdai-panel-data
```

Every restore target used the exact prefix `mlink-restore-e2e-` and loopback ports `18421` / `18422`; the test refuses the production ports `8420`, `8125`, and `8424`.

## Commands

The current-machine backup fixture is explicitly gated:

```bash
MLINK_TEST_FULL_BACKUP=1 \
MLINK_TEST_FULL_BACKUP_PATH=/private/tmp/mlink-live-test.mlink-backup \
MLINK_TEST_FULL_BACKUP_PASSPHRASE=<protected-passphrase> \
go test ./cmd/mlink -run '^TestLiveWorkspaceBackupCurrentMachine$' -count=1 -v -timeout=5m
```

The isolated restore acceptance is separately gated:

```bash
MLINK_TEST_FULL_RESTORE=1 \
MLINK_TEST_FULL_BACKUP_PATH=/private/tmp/mlink-live-test.mlink-backup \
MLINK_TEST_FULL_BACKUP_PASSPHRASE=<protected-passphrase> \
go test ./cmd/mlink -run '^TestLiveFullWorkspaceBackupRestore$' -count=1 -v -timeout=15m
```

Protected inputs are loaded from Keychain or test-process memory. They are not command arguments or test output.

## Evidence

- Full bundle size: `296,487,267` bytes.
- Full bundle SHA-256: `28e228c6499fa87337462bea8f9762d7c04b433cdff79cc3a8ed753b9d7306dd`.
- Bundle format: `mlink-full-backup/v1`.
- Encrypted sections: Core, Knowledge, MLink state, identity, credentials, and Agent selection.
- Agent selection: Codex, Cursor, Pi, and Hermes.
- First isolated restore duration: `40.69s`.
- Two-stage dynamic-Agent acceptance duration (final run): `82.38s`.
- Fixed Owner User / Team / Agent / Asset IDs matched the encrypted manifest.
- The first restored Core recalled at least one pre-existing Owner memory item through the restored identity dimensions.
- Two isolated dynamic principals were provisioned through the official metadata API (`hermes-private` and `hermes-group`), persisted to the restored Journal, backed up again, and restored into a second isolated Core.
- The second restore verified both dynamic Agent and Chat Memory Asset IDs byte-for-byte against the encrypted manifest.
- No `init-admin`, `create-user`, or replacement Owner identity call occurs during restore.
- Core secrets reached Docker through the process environment with `-e NAME`; secret values were absent from argv, Plan JSON, logs, and temporary files.
- A transient macOS `launchctl bootstrap` compensation race was reproduced. Recovery now polls the actual Broker and Provider state, retries bootstrap, and accepts a compensation error only after all original services are verified running.
- After each acceptance run, no container, volume, or network with prefix `mlink-restore-e2e-` remained.
- Formal `tdai-memory-core`, `tdai-memory-hub`, and `dev.mlink.broker` were healthy/running after the test.

## Safety assertions

The live fixture performs exact-name cleanup only. It never removes a formal production volume, never uses a wildcard Docker deletion, never changes Agent model/auth settings, and never prints restored memory content or credentials.
