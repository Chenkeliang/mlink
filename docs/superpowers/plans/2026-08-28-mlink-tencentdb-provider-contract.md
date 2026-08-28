# MLink TencentDB Provider Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first executable MLink slice: canonical identity models plus a TencentDB MemoryCore v3 provider whose scope claims are enforced by unit and live contract tests.

**Architecture:** A small Go package owns canonical MLink requests and results. A TencentDB package translates those models to the official MemoryCore v3 HTTP API, always sends explicit identity fields, treats L1 as user-scoped, and exposes L2/L3 only as opt-in Agent-shared context. Live tests target an externally managed MemoryCore instance and never patch or configure that instance.

**Tech Stack:** Go 1.22, Go standard library (`net/http`, `httptest`, `encoding/json`, `testing`), TencentDB MemoryCore v3 HTTP API.

**Spec:** `docs/superpowers/specs/2026-08-28-mlink-mvp-design.md`

## Global Constraints

- MLink never proxies or changes an Agent's LLM provider, model, base URL, subscription, login, API key, or authentication path.
- Every memory operation carries explicit `tenant_id`, `user_id`, and `agent_id`; capture additionally requires `session_id` and `turn_id`.
- Missing identity is rejected locally and is never replaced with `default`.
- TencentDB L1 is user-scoped; current official L2/L3 is Agent-shared and must never be advertised as user-private.
- Agent-shared L2/L3 is excluded from default recall and included only when `include_agent_shared` is explicitly true.
- This slice does not implement Broker, Journal, JSON-RPC Provider Host, TUI, or Agent installers.
- Live tests must use environment variables for endpoint and token; secrets are never committed, logged, or passed in command-line arguments.
- Existing user changes in `README.md` are out of scope and must not be staged or modified.

---

### Task 1: Canonical identity and context models

**Files:**
- Create: `go.mod`
- Create: `internal/model/model.go`
- Create: `internal/model/model_test.go`

**Interfaces:**
- Produces: `model.IdentityScope`, `model.Turn`, `model.RecallRequest`, `model.ContextItem`, `model.ContextBundle`, `model.WriteReceipt`.
- Produces: `IdentityScope.ValidateForRecall() error` and `IdentityScope.ValidateForCapture() error`.
- Produces: `model.ScopeUser` and `model.ScopeAgent` constants used by the Provider.

- [ ] **Step 1: Create the module and write failing identity validation tests**

Create `go.mod`:

```go
module mlink

go 1.22
```

Create table-driven tests in `internal/model/model_test.go` using literal inputs. The tests must prove that recall rejects each missing long-term identity field, capture additionally rejects missing session and turn IDs, and a complete identity succeeds:

```go
func TestIdentityScopeValidateForRecall(t *testing.T) {
	valid := IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"}
	// Cases independently clear TenantID, UserID, and AgentID and expect an error.
	// The unchanged literal must return nil.
}

func TestIdentityScopeValidateForCapture(t *testing.T) {
	valid := IdentityScope{
		TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
		SessionID: "session-a", TurnID: "turn-a",
	}
	// Cases independently clear all five fields and expect an error.
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/model -run TestIdentityScope -count=1
```

Expected: compilation fails because `IdentityScope` and its validation methods do not exist.

- [ ] **Step 3: Implement the minimal canonical models**

Create `internal/model/model.go` with these exact public shapes:

```go
package model

import (
	"errors"
	"time"
)

type ScopeKind string

const (
	ScopeUser  ScopeKind = "user"
	ScopeAgent ScopeKind = "agent"
)

type IdentityScope struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
	UserID       string `json:"user_id"`
	AgentID      string `json:"agent_id"`
	SessionID    string `json:"session_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
}

type Message struct {
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Turn struct {
	Identity IdentityScope `json:"identity"`
	Messages []Message     `json:"messages"`
}

type RecallRequest struct {
	Identity           IdentityScope `json:"identity"`
	Query              string        `json:"query"`
	MaxItems           int           `json:"max_items"`
	IncludeAgentShared bool          `json:"include_agent_shared"`
}

type ContextItem struct {
	ID        string        `json:"id"`
	Kind      string        `json:"kind"`
	Scope     ScopeKind     `json:"scope"`
	Text      string        `json:"text"`
	CreatedAt *time.Time    `json:"created_at,omitempty"`
	UpdatedAt *time.Time    `json:"updated_at,omitempty"`
	Score     *float64      `json:"score,omitempty"`
	Source    string        `json:"source"`
}

type ContextBundle struct {
	Items    []ContextItem `json:"items"`
	Partial  bool          `json:"partial"`
	Warnings []string      `json:"warnings,omitempty"`
}

type WriteReceipt struct {
	AcceptedIDs []string `json:"accepted_ids"`
}

var ErrInvalidIdentity = errors.New("invalid identity scope")
```

Implement validation by checking trimmed values and wrapping `ErrInvalidIdentity` with the exact missing field name. Do not generate IDs or insert defaults.

- [ ] **Step 4: Run model tests and verify GREEN**

Run:

```bash
go test ./internal/model -count=1
```

Expected: all model tests pass.

- [ ] **Step 5: Commit Task 1 files only**

```bash
git add go.mod internal/model/model.go internal/model/model_test.go
git commit -m "feat: add canonical memory identity models"
```

---

### Task 2: MemoryCore v3 transport and error contract

**Files:**
- Create: `internal/provider/tencentdb/client.go`
- Create: `internal/provider/tencentdb/client_test.go`

**Interfaces:**
- Consumes: canonical identity fields from Task 1.
- Produces: `tencentdb.Config`, `tencentdb.Client`, `tencentdb.NewClient(Config) (*Client, error)`.
- Produces: internal `Client.post(ctx, path, request, response) error` used by later Provider methods.
- Produces: `tencentdb.APIError` with `HTTPStatus`, `Code`, and `Message`.

- [ ] **Step 1: Write failing transport tests against a real `httptest.Server`**

Tests must name these breaks:

1. A request without `Authorization: Bearer <token>` or `x-tdai-service-id` must fail the test.
2. The client must POST JSON to the exact v3 path and decode `data` from `{code,message,request_id,data}`.
3. HTTP 401 and non-zero envelope codes must return an `APIError` without including the token.
4. A canceled context must stop the request.

Use a real `httptest.Server`; do not mock `http.Client.Do`.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./internal/provider/tencentdb -run TestClient -count=1
```

Expected: compilation fails because `Config`, `Client`, and `NewClient` do not exist.

- [ ] **Step 3: Implement the minimal v3 transport**

Use these exact public types:

```go
type Config struct {
	BaseURL   string
	Token     string
	ServiceID string
	HTTPClient *http.Client
}

type Client struct {
	baseURL   string
	token     string
	serviceID string
	http      *http.Client
}

type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
}
```

`NewClient` must require an absolute `http` or `https` URL, non-empty token, and non-empty service ID. Strip one trailing slash from the base URL. Default to an `http.Client` with a two-second timeout when no client is supplied. `post` must use `http.NewRequestWithContext`, set both auth headers, cap response reading at 4 MiB, reject non-JSON bodies, and never include request headers or response bodies in error strings.

- [ ] **Step 4: Run transport tests and verify GREEN**

```bash
go test ./internal/provider/tencentdb -run TestClient -count=1
```

Expected: all transport tests pass.

- [ ] **Step 5: Commit Task 2 files only**

```bash
git add internal/provider/tencentdb/client.go internal/provider/tencentdb/client_test.go
git commit -m "feat: add TencentDB v3 transport"
```

---

### Task 3: Capture and scoped recall behavior

**Files:**
- Create: `internal/provider/tencentdb/provider.go`
- Create: `internal/provider/tencentdb/provider_test.go`

**Interfaces:**
- Consumes: `model.Turn`, `model.RecallRequest`, and `Client.post`.
- Produces: `tencentdb.Provider`, `tencentdb.NewProvider(*Client) *Provider`.
- Produces: `CaptureTurn(context.Context, model.Turn) (model.WriteReceipt, error)`.
- Produces: `Recall(context.Context, model.RecallRequest) (model.ContextBundle, error)`.

- [ ] **Step 1: Write failing capture tests**

Use an `httptest.Server` that decodes the actual request body. Assert observable behavior:

- `/v3/conversation/add` receives explicit `team_id`, `agent_id`, `user_id`, `session_id` and exactly the provided user/assistant messages.
- A missing identity fails before the server receives any request.
- Only `user` and `assistant` roles are accepted in this slice.
- An empty message list is rejected.
- The returned `accepted_ids` becomes `WriteReceipt.AcceptedIDs`.

- [ ] **Step 2: Run capture tests and verify RED**

```bash
go test ./internal/provider/tencentdb -run TestProviderCapture -count=1
```

Expected: compilation fails because `Provider.CaptureTurn` does not exist.

- [ ] **Step 3: Implement minimal capture**

Translate `TenantID` to MemoryCore `team_id`; pass `AgentID`, `UserID`, and `SessionID` unchanged. Translate `OccurredAt` to UTC RFC3339Nano `timestamp`. Do not send `turn_id` to MemoryCore because v3 conversation add has no such field; idempotency remains a future Broker responsibility.

- [ ] **Step 4: Run capture tests and verify GREEN**

```bash
go test ./internal/provider/tencentdb -run TestProviderCapture -count=1
```

- [ ] **Step 5: Write failing recall scope tests**

The test server must implement the exact official envelopes for:

- `/v3/atomic/search` returning `data.items`.
- `/v3/scenario/ls` returning `data.entries`.
- `/v3/scenario/read` returning `data.content`.
- `/v3/core/read` returning `data.content`.

Assert:

1. Default recall calls only `/v3/atomic/search` and marks all returned items `ScopeUser`.
2. `IncludeAgentShared=true` additionally calls L2/L3 endpoints and marks their items `ScopeAgent`.
3. All calls carry the same explicit team, Agent, and user identifiers.
4. Duplicate stable IDs in a single response are emitted once.
5. One failed shared endpoint yields `Partial=true` plus a warning while valid L1 remains available.
6. Malformed scope or an empty query is rejected before network I/O.

- [ ] **Step 6: Run recall tests and verify RED**

```bash
go test ./internal/provider/tencentdb -run TestProviderRecall -count=1
```

Expected: tests fail because `Provider.Recall` is missing.

- [ ] **Step 7: Implement minimal scoped recall**

Use `MaxItems=5` when the request supplies zero and cap it at 20. L1 calls are synchronous. When shared context is enabled, fetch L2 list and L3 core concurrently with the same request context; read listed L2 files only until the item budget is exhausted. Build stable IDs as `l1:<native-id>`, `l2:<path>`, and `l3:persona`. Dedupe only identical stable IDs; do not perform semantic dedupe or rewrite content.

- [ ] **Step 8: Run the full package tests and verify GREEN**

```bash
go test ./internal/provider/tencentdb -count=1
```

- [ ] **Step 9: Commit Task 3 files only**

```bash
git add internal/provider/tencentdb/provider.go internal/provider/tencentdb/provider_test.go
git commit -m "feat: add scoped TencentDB capture and recall"
```

---

### Task 4: Reusable Provider contract suite

**Files:**
- Create: `internal/provider/contract/contract.go`
- Create: `internal/provider/contract/contract_test.go`
- Create: `internal/provider/tencentdb/contract_test.go`

**Interfaces:**
- Consumes: any Provider implementing `CaptureTurn` and `Recall`.
- Produces: `contract.Provider` interface and `contract.Run(t, factory)` reusable by future Mem0/Hindsight providers.

- [ ] **Step 1: Write a failing contract test around a deliberately unsafe fixture**

Define the interface:

```go
type Provider interface {
	CaptureTurn(context.Context, model.Turn) (model.WriteReceipt, error)
	Recall(context.Context, model.RecallRequest) (model.ContextBundle, error)
}

type Factory func(t *testing.T) Provider
```

The unsafe fixture must return user A's item for user B. `contract.Run` must fail that fixture's `user isolation` subtest. Use an inner `testing.RunTests` invocation so the outer test can assert that the unsafe fixture is rejected.

- [ ] **Step 2: Run the contract package and verify RED**

```bash
go test ./internal/provider/contract -count=1
```

Expected: compilation fails because `contract.Run` does not exist.

- [ ] **Step 3: Implement the minimal reusable suite**

The suite must cover:

- Missing identity rejected.
- A valid new user receives an empty bundle rather than another user's data.
- User A and user B cannot see each other's `ScopeUser` items.
- Shared Agent items are absent by default.
- Repeated returned IDs are collapsed once.
- A canceled context returns promptly.

Do not make the generic suite assert TencentDB-specific paths or payloads.

- [ ] **Step 4: Verify the unsafe fixture is rejected and TencentDB fixture passes**

```bash
go test ./internal/provider/contract ./internal/provider/tencentdb -run Contract -count=1
```

- [ ] **Step 5: Commit Task 4 files only**

```bash
git add internal/provider/contract/contract.go internal/provider/contract/contract_test.go internal/provider/tencentdb/contract_test.go
git commit -m "test: add reusable provider contract suite"
```

---

### Task 5: Live MemoryCore capability and adversarial tests

**Files:**
- Create: `internal/provider/tencentdb/live_test.go`
- Create: `docs/testing/tencentdb-memorycore-v2.0.1.md`

**Interfaces:**
- Consumes environment variables `MLINK_TEST_MEMORYCORE_URL`, `MLINK_TEST_MEMORYCORE_TOKEN`, and optional `MLINK_TEST_MEMORYCORE_SERVICE_PREFIX`.
- Produces a repeatable `integration` build-tag suite and a checked-in report with no secrets or raw private conversation text.

- [ ] **Step 1: Write live tests that skip safely without credentials**

Add `//go:build integration`. A helper must call `t.Skip` unless both required variables exist. Generate unique team, Agent, user, session, turn, and service IDs per test; never use `default`.

Implement these tests against the real Provider:

```go
func TestLiveNewUserIsEmpty(t *testing.T)
func TestLiveCaptureAndEventuallyRecallL1(t *testing.T)
func TestLiveDuplicateTurnProducesNoDuplicateContextID(t *testing.T)
func TestLiveTwoUsersDoNotShareL1(t *testing.T)
func TestLiveAgentSharedLayersAreOptIn(t *testing.T)
func TestLiveMissingIdentityIsRejectedLocally(t *testing.T)
func TestLiveTransientInventoryIsNotPromotedToL1(t *testing.T)
```

Polling for eventual L1 visibility must use a context deadline of two minutes, a two-second interval, and report the final pipeline/API observation on failure. Do not sleep unconditionally after visibility is reached.

- [ ] **Step 2: Run the live suite and verify it fails for the intended missing implementation or incorrect scope behavior**

```bash
MLINK_TEST_MEMORYCORE_URL=http://127.0.0.1:8420 \
MLINK_TEST_MEMORYCORE_TOKEN="$MLINK_TEST_MEMORYCORE_TOKEN" \
go test -tags=integration ./internal/provider/tencentdb -run TestLive -count=1 -v
```

Expected before the preceding tasks are complete: compile or behavior failure. After Tasks 1–4, any remaining failure is a real Provider/backend contract finding and must not be patched around silently.

- [ ] **Step 3: Record the exact backend capability result**

Write `docs/testing/tencentdb-memorycore-v2.0.1.md` with:

- MemoryCore image/tag and source commit.
- LLM endpoint host and model name, never the key.
- L1 user isolation result.
- L2/L3 Agent-shared scope result.
- Service-ID isolation result.
- Duplicate L0 versus deduplicated L1 result.
- Temporal inventory result for L0 and L1.
- Restart persistence and latency measurements.
- Official Hermes provider `data.messages` versus `data.items` mismatch as a separate upstream defect.
- A final capability table that marks user-private L1 as supported, user-private L2/L3 as unsupported, and Agent-shared L2/L3 as opt-in only.

- [ ] **Step 4: Run all unit and live verification commands**

```bash
go test ./... -count=1
MLINK_TEST_MEMORYCORE_URL=http://127.0.0.1:8420 \
MLINK_TEST_MEMORYCORE_TOKEN="$MLINK_TEST_MEMORYCORE_TOKEN" \
go test -tags=integration ./internal/provider/tencentdb -run TestLive -count=1 -v
go vet ./...
gofmt -d .
```

Expected: unit tests and vet pass; `gofmt -d .` prints nothing. Live failures are acceptable only when the checked-in capability report marks the corresponding Provider profile unsupported and default recall excludes that scope.

- [ ] **Step 5: Commit Task 5 files only**

```bash
git add internal/provider/tencentdb/live_test.go docs/testing/tencentdb-memorycore-v2.0.1.md
git commit -m "test: characterize TencentDB MemoryCore capabilities"
```

---

## Self-review result

- Spec coverage for this slice: canonical identity, explicit v3 mapping, truthful L1/L2/L3 scopes, no default identity, response-ID dedupe, fail-open partial recall, and real MemoryCore adversarial coverage are assigned to concrete tasks.
- Deliberately deferred to separate plans: Provider Host framing/lifecycle, Broker/Journal, Adapter API/auth, Codex/Pi/Hermes adapters, TUI/install/uninstall, Provider switching, portable identity backup, and malicious Provider process tests.
- Placeholder scan: no TBD/TODO/"implement later" steps remain.
- Type consistency: Tasks 2–5 consume the exact model and Provider signatures defined in Tasks 1 and 3.
