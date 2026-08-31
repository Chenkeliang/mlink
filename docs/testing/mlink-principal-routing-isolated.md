# MLink Principal Routing Isolated Verification

Date: 2026-08-31

Status: offline acceptance passed; race-enabled builds remain partially blocked by the local Xcode architecture mismatch.

## Scope

All checks ran against in-memory targets, temporary directories, fake Keychain stores, local HTTP servers, generated Hermes Provider source, and temporary SQLite Journals. They did not read or modify real Codex, Pi, Hermes, LaunchAgent, Keychain, or MemoryCore state.

## Identity and routing results

- Two active Feishu aliases were interleaved 100 times and always resolved to the same `usr_owner_keliang` Principal and `personal-owner` Space.
- Reconstructing the Router from schema-v2 config plus Keychain values preserved the owner identity.
- Revoking the old alias made it fail closed while the new alias retained owner access.
- An identity that had never been bound routed to an isolated `hermes-private` user and could not become owner.
- Two actors in one group topic were interleaved 100 times and always shared the same group Principal and topic Session while retaining different actor digests.
- Two topics in one group shared one group Principal but used different Sessions.
- Different groups used different Principals and Sessions.
- A group card event without `thread_id` safely fell back to the group-main Session while retaining the same group Principal.
- Missing group `chat_id`, missing DM subject, and unsupported chat type failed closed.
- Codex and Pi remained fixed owner consumers and required no Feishu identifier.

## Configuration and installation results

- Schema v2 separates one TencentDB Connection from `personal-owner`, `hermes-private`, and `hermes-groups` Memory Spaces.
- The owner canonical user and Binding slot are deterministic non-secret plan inputs.
- Changing the MemoryCore Token, owner Binding value, or pre-Apply Identity Key did not change the installation Plan ID.
- Applying an install stored Token, Identity Key, Hermes Grant, and owner Binding transactionally.
- Full uninstall removed every MLink Keychain item and restored both Hermes Session-sharing flags plus later unrelated user configuration.
- Identity bind/rebind/revoke used ChangeSets and exact Plan IDs; rebind retained the revoked raw value only in the fake Keychain rejection set.
- TUI owner selection required an explicit candidate choice even when two candidates had the same display name.
- TUI views at 72, 100, and 140 columns did not render the raw candidate identifier.

## Encryption and migration results

- Identity Bundle round-trip used scrypt (`N=32768`, `r=8`, `p=1`) and AES-256-GCM.
- A wrong passphrase and a one-bit ciphertext change returned the authentication error.
- Encrypted output contained neither raw Binding values nor the Identity Key.
- Bundle files were written atomically with mode `0600`.
- Bundle, Binding, Candidate, and request debug formatting redacted identity material.
- Import rejected a different owner canonical user before mutation.
- Exporting an installed test identity preserved the owner canonical user and Binding.

## Journal and leakage results

- Delegated Hermes Turn IDs include actor digest before Journal insertion, preventing cross-actor collisions in a shared group Session.
- Actor digest is stored in its own Journal column and excluded from Provider Turn JSON.
- Raw Feishu alias, group ID, and topic ID canaries did not appear in canonical identities or closed SQLite Journal bytes.
- Existing event replay, changed-content conflict, ambiguous delivery, payload clearing, retry, rollback, and stale-plan tests remained passing.

## Verification commands

Passed:

```text
go test ./...
go test -shuffle=on -count=2 ./...
go vet ./...
GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 go test ./...
```

Race attempt:

```text
go test -race ./internal/identity ./internal/broker ./internal/journal ./internal/e2e
```

`internal/identity` passed under the race detector. The packages requiring cgo did not compile because local Xcode's `libxcrun.dylib` is `arm64` while the invoking process requires `arm64e`. No race-enabled project test failed because those binaries were not produced. System Xcode settings were not changed.

## Guarantee boundary

These results prove MLink's identity mapping, authorization, idempotency, rollback, encryption, and no-raw-ID boundaries offline. They do not yet prove TencentDB semantic extraction or alias continuity against the isolated live backend. That is the next gated task and uses unique service/team/Agent/user identifiers only.
