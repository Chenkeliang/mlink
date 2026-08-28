# TencentDB MemoryCore v2.0.1 Contract Report

Date: 2026-08-28  
Status: Tested against an isolated local instance; not approved as a user-private L2/L3 backend.

## Test target

| Item | Value |
|---|---|
| Upstream | `TencentCloud/TencentDB-Agent-Memory` |
| Tag | `v2.0.1` |
| Source commit | `a5dcbe6e9fee0d1d1e32d935326f1d3bcf927fdb` |
| Local image | `mlink-memorycore-test:v2.0.1` |
| Gateway | `http://127.0.0.1:8420` |
| LLM endpoint | `https://llm.dedao.cn/v1` |
| LLM model | `deepseek-v4-pro` |
| Strict isolation | `V3_STRICT_ISOLATION=true` |
| Embedding | Disabled |
| Retrieval | BM25 / SQLite FTS |
| Data volume | `mlink-memorycore-test-data` |

No Agent model provider, model URL, subscription, login, or API credential was modified. The MemoryCore LLM credential was read from the existing OrbStack Hermes memory configuration and was not printed or committed.

## Automated MLink live suite

Command shape:

```bash
MLINK_TEST_MEMORYCORE_URL=http://127.0.0.1:8420 \
MLINK_TEST_MEMORYCORE_TOKEN="$MLINK_TEST_MEMORYCORE_TOKEN" \
go test -tags=integration ./internal/provider/tencentdb -run TestLive -count=1 -v
```

The token value is supplied through the process environment and is never placed in the repository.

| Test | Result | Observation |
|---|---|---|
| New identity | Pass | A valid unseen user returns an empty L1 bundle. |
| L0 capture to cross-session L1 recall | Pass | Ten messages captured in session A became a visible user-scoped L1 memory when recalled from session B in about 21 seconds. Recall does not send `session_id`. |
| Repeated-content response IDs | Pass | MLink returned every stable Context ID at most once. This does not claim replay-safe capture. |
| Two-user L1 isolation | Pass | After user A's L1 canary was confirmed visible, user B could not recall it under the same Provider process. |
| Shared-layer policy and hierarchy | Pass | Default recall returned no Agent-scoped items; explicit opt-in saw L2/L3 after about 125 seconds. A second user under the same Agent could read the shared layer; a different Agent could not. |
| Missing identity | Pass | MLink rejected the request locally without falling back to `default`. |
| Transient inventory | Pass | Ten raw messages containing current stock `104` produced zero L1 memories; L1 extraction completed in about 8 seconds. |

Full post-review live suite result: 7 tests passed in 195.802 seconds. Every stateful test used a unique service/team/Agent/user fixture. Running the integration suite without URL/token now safely skips all seven tests instead of failing.

The corresponding offline tests also verify that cancellation/deadline errors are propagated, L2 wins deterministically over L3 when only one shared slot remains, L2 reads cannot exceed the remaining item budget, empty native IDs are rejected, non-JSON HTTP 401 responses retain their typed status, and upstream error messages cannot be reflected into MLink errors.

## Direct backend adversarial results

These checks call the official v3 Gateway API directly so Provider behavior and backend behavior remain distinguishable.

### Duplicate records

- Ten repeated PayPal reminders were all retained in L0.
- L1 extraction produced one instruction memory.
- Result: MemoryCore has semantic consolidation at L1, but L0 capture is not idempotent.
- MLink must not claim Provider replay safety. Event replay prevention belongs to the future Broker Journal.

### Temporal inventory

- L0 search for current JD inventory returned current `0` together with historical `104` and `63`.
- L1 did not preserve a transient numeric inventory snapshot by itself.
- When the user explicitly stated a durable rule, L1 stored the rule that current inventory must come from a real-time source and that `104`/`63` are expired examples.
- Result: raw L0 is historical evidence, not a current-fact source. Default MLink recall must not inject L0.

### Multi-user and hierarchy

| Probe | Observed result |
|---|---|
| User B reads user A L1 under same Agent | Empty; isolated |
| Unseen user reads user A L3 under same Agent | L3 returned |
| Different Agent reads user A L3 | Empty |
| Different `x-tdai-service-id` with same team/Agent reads L3 | L3 returned |
| Missing `user_id` with strict mode | HTTP 422 |

Current L2/L3 storage scope is effectively `team_id + agent_id`; it is not user-private and is not fully partitioned by standalone `service-id`. MLink therefore labels these layers `ScopeAgent` and excludes them from default personal recall.

### Profile inference quality

The current L2/L3 prompts and `deepseek-v4-pro` generated unsupported inferences from sparse evidence. Examples observed in isolated fixtures include inferring cross-border financial activity from a PayPal reminder, inferring that a user is an e-commerce/warehouse operator from an inventory rule, and inferring past trust damage from a request for real-time data.

These are Provider/backend quality findings, not Embedding behavior. MLink must preserve source and scope and must not present generated L2/L3 prose as verified user facts.

### Restart persistence

After `docker restart mlink-memorycore-test`, health returned `healthy`; six previously extracted L1 items and the associated L3 profile remained readable from the named volume.

## Official Hermes provider defect

MemoryCore v2.0.1 returns raw conversation search results in `data.messages`. The official Hermes provider's `memory_tencentdb_conversation_search` handler reads `data.items`, so the explicit raw-conversation tool reports no results even when the Gateway returned matches.

Relevant upstream files in the pinned checkout:

- `MemoryCore/hermes-plugin/memory/memory_tencentdb/client.py`: `conversation_search` calls `/v3/conversation/search`.
- `MemoryCore/hermes-plugin/memory/memory_tencentdb/__init__.py`: tool handler reads `result.get("data", {}).get("items", [])`.

Automatic Hermes `prefetch` does not use L0. It calls L1 atomic search plus L2/L3 reads, so this mismatch affects the explicit conversation-history tool rather than normal prefetch.

## Capability decision

| Capability | Status | MLink policy |
|---|---|---|
| Health | Supported | Expose normally |
| Capture turn to L0 | Supported, not replay-safe | Broker must journal and prevent duplicate sends |
| User-private L1 | Supported | Enabled by default |
| Stable response-ID dedupe | Supported by MLink | Remove exact duplicate IDs only |
| User-private L2 | Unsupported | Never advertise as personal memory |
| User-private L3 | Unsupported | Never advertise as personal profile |
| Agent-shared L2/L3 | Supported with warning | Explicit opt-in only |
| Standalone Service-ID isolation for L3 | Unsupported | Connection namespace must prevent identifier collision; do not claim backend partitioning |
| Temporal current-fact semantics | Unsupported for L0 | Do not use L0 for current inventory or other live operational facts |
| Raw conversation search through official Hermes tool | Defective in v2.0.1 | Report upstream; do not patch in this baseline |
| Embedding retrieval | Not tested | Current instance intentionally uses BM25 |

## Acceptance conclusion

TencentDB MemoryCore v2.0.1 is acceptable for MLink's user-private L1 profile only when every request carries explicit stable identity. L2/L3 can be offered solely as clearly labelled Agent-shared context behind an opt-in policy. The backend does not qualify for MLink's user-private L2/L3 or full standalone Service-ID isolation profiles in its current form.
