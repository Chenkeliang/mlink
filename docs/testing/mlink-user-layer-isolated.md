# MLink User-Layer Isolated Verification

Date: 2026-08-28

## Scope

The suite exercises the MLink user-layer without reading or changing the real Codex, Pi, Hermes, LaunchAgent, Keychain, or MemoryCore state.

Covered boundaries:

- preview performs no writes;
- exact ChangeSet freshness is enforced before Apply;
- install and full uninstall restore an unchanged target byte-for-byte;
- changed MLink-owned files stop uninstall instead of being overwritten;
- Codex and Hermes semantic restore paths preserve later unrelated changes;
- repeated events are deduplicated by Adapter, route, identity, turn identity, and content hash;
- a reused turn identity with changed content is rejected;
- retryable, permanent, and ambiguous delivery states remain distinct;
- ambiguous delivery retains its payload and blocks destructive state removal;
- two delegated Feishu users remain isolated across 100 interleaved requests;
- a delegated request without a stable Feishu identity fails closed;
- recalled text containing a fake system instruction remains below an explicit untrusted-data boundary;
- TUI Apply is unreachable until the exact preview has a second confirmation.

## Results

- `go test ./...`: passed.
- `go test -shuffle=on -count=2 ./...`: passed twice.
- `GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 go test ./...`: passed.
- `go vet ./...`: passed.
- `go test -race ./internal/journal ./internal/broker ./internal/e2e`: blocked before compilation by the local Xcode `arm64`/`arm64e` `libxcrun.dylib` mismatch. No project test failed under the race detector because the race-enabled binaries were not produced.

## Guarantee Boundary

The SQLite Journal prevents MLink from dispatching the same confirmed event again and detects conflicting reuse of a turn identity. TencentDB MemoryCore does not advertise replay-safe capture, so a response lost after send is classified as `ambiguous` and is not automatically replayed. MLink therefore does not claim end-to-end exactly-once semantic memory creation.

Semantic consolidation, fact supersession, expiration, and retrieval ranking remain MemoryCore behavior. MLink does not add a second semantic memory engine.
