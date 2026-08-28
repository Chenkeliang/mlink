# MLink Provider Host Implementation Plan

> **Execution mode:** inline, test-driven implementation on `feat_product_design`.

**Goal:** Put the existing TencentDB memory Provider behind MLink's versioned, language-neutral process boundary without adding Broker, Journal, Agent adapters, or Provider installation.

**Architecture:** A validated immutable `ConnectionSnapshot` selects a bundled Provider manifest. A `host.Session` launches one Provider process for that exact route revision, negotiates capabilities, and exchanges Content-Length framed JSON-RPC 2.0 messages. A reusable `server.Server` dispatches those messages to a business `Handler`; the bundled TencentDB handler wraps the existing HTTP Provider. The process protocol remains independent of TencentDB and is verified with a Python fixture.

**Tech stack:** Go 1.22, standard library process/JSON/concurrency packages, `gopkg.in/yaml.v3`, Python 3 test fixture, existing TencentDB v3 client.

**Out of scope:** Broker API, SQLite Journal, Agent hooks, TUI, package download/install, automatic Provider restart/fuse, Provider switching, second production Provider, third-party production execution.

---

## Task 1: Canonical write receipt and immutable connection route

**Files:**

- Modify: `internal/model/model.go`
- Modify: `internal/model/model_test.go`
- Modify: `internal/provider/tencentdb/provider.go`
- Modify: `internal/provider/tencentdb/provider_test.go`
- Create: `internal/connection/snapshot.go`
- Create: `internal/connection/snapshot_test.go`

**RED:**

Add tests proving:

- `WriteReceipt` exposes `receipt_id`, `state`, `provider_refs`, `replay_safe`, and warnings.
- `NewSnapshot` rejects every empty/invalid route component.
- connection/revision accept only 1–128 ASCII `[A-Za-z0-9._-]`.
- Provider ID is reverse-domain form and Provider version is SemVer 2.0.
- Config and SecretRefs are deep-copied both into and out of the snapshot.
- tenant/user/agent/session/turn never enter `RouteKey`.
- TencentDB maps `accepted_ids` to `ProviderRefs`, returns `accepted`, and remains non-replay-safe.

Run:

```bash
go test ./internal/model ./internal/connection ./internal/provider/tencentdb
```

Expected: compile/test failure because the new canonical types and connection package do not exist.

**GREEN:**

Implement:

```go
type WriteState string

const (
    WriteAccepted WriteState = "accepted"
    WriteVisible  WriteState = "visible"
)

type WriteReceipt struct {
    ReceiptID    string     `json:"receipt_id"`
    State        WriteState `json:"state"`
    ProviderRefs []string   `json:"provider_refs,omitempty"`
    ReplaySafe   bool       `json:"replay_safe"`
    Warnings     []string   `json:"warnings,omitempty"`
}
```

Create `connection.ProviderRef`, `ConnectionSnapshot`, `RouteKey`, `NewSnapshot`, `RouteKey()`, `Config()`, and `SecretRefs()` with construction-time validation and defensive copies. Use no external SemVer dependency; implement only strict SemVer 2.0 validation required by the route.

Run the focused tests until green, then run `go test ./...`.

**Commit:** `feat: add immutable provider route model`

## Task 2: Manifest parsing, bundled registry, and capability validation

**Files:**

- Modify: `go.mod`
- Create: `go.sum`
- Create: `internal/provider/manifest/manifest.go`
- Create: `internal/provider/manifest/capability.go`
- Create: `internal/provider/manifest/registry.go`
- Create: `internal/provider/manifest/manifest_test.go`
- Create: `internal/provider/manifest/capability_test.go`
- Create: `internal/provider/manifest/registry_test.go`

**RED:**

Add table tests for:

- valid TencentDB manifest parsing;
- 1 MiB file limit, unknown fields, anchors, aliases, custom tags, duplicate protocol versions, scalar/shell entrypoint, empty required fields;
- config schema path containment after symlink resolution;
- `$()`, glob, and semicolon preserved as inert argv values;
- exact bundled registry match for `mlink provider run tencentdb`;
- rejection of a bundled manifest targeting any other command;
- required descriptor fields and `MaxInFlight` normalization from zero to four;
- runtime capabilities accepted only when they are a subset of declared capabilities.

Run:

```bash
go test ./internal/provider/manifest
```

Expected: package missing.

**GREEN:**

Use `yaml.v3.Decoder.KnownFields(true)`, inspect the YAML node tree before decode, and resolve resource paths relative to the manifest directory. Expose:

```go
func Load(path string) (Manifest, error)
func ValidateRuntime(declared, runtime map[string]CapabilityDescriptor) error
func ResolveBundled(manifest Manifest, currentExecutable string) ([]string, error)
```

Return defensive copies of capability maps. Do not add production loading for arbitrary executables.

Run focused tests and `go test ./...`.

**Commit:** `feat: validate provider manifests and capabilities`

## Task 3: Content-Length JSON-RPC codec and protocol types

**Files:**

- Create: `internal/provider/protocol/types.go`
- Create: `internal/provider/protocol/codec.go`
- Create: `internal/provider/protocol/codec_test.go`
- Create: `internal/provider/protocol/types_test.go`

**RED:**

Add tests covering:

- Chinese/multiline payloads, fragmented reads, and consecutive frames;
- case-insensitive single `Content-Length`;
- 8 KiB header and 4 MiB payload limits;
- missing/duplicate/negative/non-decimal length, truncated body, invalid UTF-8, and stdout garbage;
- rejection of JSON arrays, non-`2.0`, invalid request IDs, and responses with both/neither `result` and `error`;
- decimal string IDs retained exactly beyond JavaScript's safe integer range;
- concurrent writers never interleave frames.

Run `go test ./internal/provider/protocol`; expect missing package.

**GREEN:**

Implement a bounded `Decoder` and serialized `Encoder`. Define request, response, notification, RPC error, request metadata, initialize params/result, health result, and stable error codes. Decode first to a strict envelope and then to method/result-specific payloads; never log raw frames.

Run focused tests and `go test ./...`.

**Commit:** `feat: add bounded provider JSON-RPC codec`

## Task 4: Provider Server lifecycle and cancellation

**Files:**

- Create: `internal/provider/server/handler.go`
- Create: `internal/provider/server/server.go`
- Create: `internal/provider/server/server_test.go`

**RED:**

Create an in-memory duplex test handler and prove:

- `initialize` must be the first request and can run only once;
- health/capture/recall dispatch only after initialization;
- protocol deadlines become handler contexts;
- `$/cancelRequest` cancels the matching handler context;
- handler concurrency is bounded;
- only one writer emits stdout frames;
- shutdown rejects new calls, waits for active calls up to its context, and invokes handler cleanup;
- unknown methods and malformed params return stable errors without leaking payloads.

Run `go test ./internal/provider/server`; expect missing package.

**GREEN:**

Define a language-neutral `Handler` interface and implement `Server.Serve(ctx, stdin, stdout)`. Store per-request cancel functions keyed by decimal string ID. Convert handler errors to the stable RPC error vocabulary. Keep TencentDB imports out of this package.

Run focused tests, `go test -race ./internal/provider/server`, then `go test ./...`.

**Commit:** `feat: add provider protocol server`

## Task 5: Host security primitives and process launcher

**Files:**

- Create: `internal/provider/host/types.go`
- Create: `internal/provider/host/environment.go`
- Create: `internal/provider/host/environment_test.go`
- Create: `internal/provider/host/redactor.go`
- Create: `internal/provider/host/redactor_test.go`
- Create: `internal/provider/host/process_unix.go`
- Create: `internal/provider/host/process_unix_test.go`

**RED:**

Add tests proving:

- only PATH, LANG, and LC_* are inherited;
- Provider-specific HOME/TMPDIR exist with mode `0700` and differ from the real HOME;
- Agent/model/auth environment canaries are absent;
- split-chunk secrets are redacted before entering the 64 KiB diagnostic ring;
- every JSON string in a response is scanned after unescaping and secret echo is detected;
- the launcher uses argv directly, has a fixed package working directory, and owns a distinct Unix process group;
- termination targets only the exact process group it created.

Run `go test ./internal/provider/host`; expect missing package.

**GREEN:**

Implement state/error/delivery/exit-event types, minimal environment construction, revision directories, streaming exact-secret redaction, recursive JSON string scanning, and Unix process-group helpers. Keep launcher test seams package-private; do not add arbitrary production command execution.

Run focused tests and `go test -race ./internal/provider/host`.

**Commit:** `feat: add provider process security boundary`

## Task 6: Host Session initialization and normal calls

**Files:**

- Create: `internal/provider/host/session.go`
- Create: `internal/provider/host/session_test.go`
- Create: `internal/provider/host/testdata/helper_provider.go`

**RED:**

Using a Go helper subprocess, prove:

- state transitions `new → starting → initializing → ready → stopping → stopped`;
- crypto-random session nonce plus monotonic counter creates non-overridable request IDs;
- protocol version, Provider ID/version, capability subset, and secret-echo checks gate readiness;
- route, config, and secrets are present only in initialize;
- `Health`, `CaptureTurn`, and `Recall` enforce capability, connection ID, request-size, deadline, idempotency, and result validation;
- capture receipt ID equals protocol request ID and replay safety cannot be elevated;
- two observers receive the same `Done()` closure and immutable `ExitEvent`.

Run focused test names; expect compile failure.

**GREEN:**

Implement `Session` and `Start`. Use one reader goroutine, one writer goroutine, a bounded pending map/semaphore, and a bounded outbound channel. Session, rather than callers, constructs every `RequestMeta`. Validate results before returning them.

Run focused tests, `go test -race ./internal/provider/host`, and `go test ./...`.

**Commit:** `feat: run provider sessions out of process`

## Task 7: Cancellation, backpressure, ambiguous writes, and shutdown faults

**Files:**

- Modify: `internal/provider/host/session.go`
- Modify: `internal/provider/host/session_test.go`
- Modify: `internal/provider/host/testdata/helper_provider.go`

**RED:**

Add deterministic barrier-based tests for:

- queue-full and in-flight-full calls time out without a pending leak;
- queued cancellation reports `not_sent` and writer skips the item;
- cancellation after writing begins reports `ambiguous_result/maybe_sent`;
- cancellation after write acknowledgement reports `ambiguous_result/sent`;
- cancel notification is best-effort and cannot block caller return;
- late/duplicate responses are dropped, while never-issued IDs fault the Session;
- a Provider that stops reading stdin triggers the two-second-or-earlier write watchdog;
- process exit releases every pending caller;
- graceful shutdown, timeout termination, and forced process-group kill;
- Session never retries capture.

Run the focused tests repeatedly with `-count=50`; expect failures before implementation.

**GREEN:**

Add atomic outbound states `queued`, `writing`, `written`, and `canceled`. Remove pending entries and release capacity on every terminal path. Return `CallError` only with stable safe fields. Close pipes and terminate the owned process group to unblock a stuck writer.

Run:

```bash
go test -race ./internal/provider/host
go test ./internal/provider/host -run 'Cancel|Queue|Watchdog|Shutdown' -count=50
go test ./...
```

**Commit:** `feat: define provider delivery and cancellation semantics`

## Task 8: Cross-language Python contract fixture

**Files:**

- Create: `internal/provider/host/testdata/python_provider.py`
- Create: `internal/provider/host/python_contract_test.go`

**RED:**

Add lifecycle and adversarial subprocess tests for:

- initialize → health → capture → recall → shutdown;
- request IDs beyond JavaScript-safe integer range remain decimal strings;
- cancellation reaches Python;
- garbage stdout, oversized frame, wrong identity/version, capability escalation, secret echo, and split stderr secret are rejected safely;
- missing `python3` is a deterministic test failure with installation guidance.

Run `go test ./internal/provider/host -run Python`; expect missing fixture failures.

**GREEN:**

Implement a standard-library-only Python fixture using the same Content-Length framing. Expose malicious modes only through test arguments and a test-only launcher path. Do not add a production third-party Provider loader.

Run focused tests and `go test -race ./internal/provider/host`.

**Commit:** `test: verify cross-language provider protocol`

## Task 9: TencentDB Server handler and CLI subcommand

**Files:**

- Modify: `internal/provider/tencentdb/client.go`
- Modify: `internal/provider/tencentdb/client_test.go`
- Create: `internal/provider/tencentdb/handler.go`
- Create: `internal/provider/tencentdb/handler_test.go`
- Create: `cmd/mlink/main.go`
- Create: `cmd/mlink/main_test.go`

**RED:**

Add tests proving:

- config accepts only `base_url`, `service_id`, and timeout 100 ms–30 s;
- token comes only from initialize secrets;
- HTTPS and loopback HTTP are accepted, non-loopback HTTP rejected;
- initialize returns exact TencentDB identity/version and descriptors;
- `/health` maps only to safe process/config/backend states;
- capture uses protocol request ID as receipt ID, maps accepted IDs, and returns replay-safe false;
- recall preserves the canonical model;
- CLI accepts only `mlink provider run tencentdb`, does not inspect Agent config or require model environment, and serves stdin/stdout.

Run focused tests; expect missing handler/command failures.

**GREEN:**

Add a safe `Client.Health`, the TencentDB server handler, and a minimal command dispatcher. Construct the existing client once during initialize and reuse it. Ensure MemoryCore LLM/embedding configuration remains outside this handler.

Run focused tests and `go test ./...`.

**Commit:** `feat: expose bundled TencentDB provider process`

## Task 10: Real bundled subprocess integration

**Files:**

- Create: `internal/provider/host/tencentdb_process_test.go`
- Create: `internal/provider/host/testdata/tencentdb-provider.yaml`

**RED:**

Build `cmd/mlink` into a temporary directory and start it through the exact bundled registry. Back it with `httptest.Server` implementing `/health`, `/v3/conversation/add`, and `/v3/atomic/search`. Assert initialize → health → capture → recall → shutdown, explicit identity, stdin-only token, and unchanged parent Agent/model environment canaries.

Run `go test ./internal/provider/host -run TencentDBProcess`; expect missing wiring failure.

**GREEN:**

Wire the manifest, snapshot, launcher, Session, and CLI without adding special TencentDB behavior to `host`. Ensure the manifest invokes only the exact bundled command.

Run focused integration, `go test -race ./...`, and `go vet ./...`.

**Commit:** `test: verify bundled TencentDB provider process`

## Task 11: Live smoke test and final verification

**Files:**

- Create: `internal/provider/host/live_test.go`
- Modify: `docs/testing/tencentdb-memorycore-v2.0.1.md`

**RED/GREEN:**

Add an `integration`-tagged live test that launches the built subprocess against the existing isolated local MemoryCore. It must pass the token only through initialize stdin, use a unique tenant/user/agent/session/turn canary, capture one full turn, poll recall within a bounded time, verify a second user cannot retrieve the private canary, and shut down cleanly.

Run all local checks:

```bash
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
git diff --check
```

Then run the live smoke test with the existing local test credentials and record exact commands, elapsed time, pass/fail, MemoryCore version, and limitations in the test report. Scan tracked changes and captured test output for the secret canary. Confirm Codex, Pi, and Hermes model/auth configuration hashes remain unchanged.

**Commit:** `test: validate Provider Host against live MemoryCore`

## Final architecture review

Before reporting completion:

1. Compare every acceptance criterion in `docs/superpowers/specs/2026-08-28-mlink-provider-host-design.md` with a named test or an explicit deferred scope.
2. Confirm `internal/provider/protocol`, `server`, `host`, `manifest`, and `connection` do not import TencentDB.
3. Confirm production bundled resolution cannot launch arbitrary commands.
4. Confirm no distribution wrapper, Broker, Journal, Agent adapter, TUI, restart/fuse, or second Provider slipped into this slice.
5. Confirm the user's pre-existing `README.md` modification remains unstaged and uncommitted.
6. Run branch-history sanity checks against the production base before proposing integration.
