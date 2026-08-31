# MLink TencentDB Panel and Core-Generated Identity Design

Date: 2026-08-31

Status: proposed for user review

## 1. Purpose

Replace MLink's client-generated TencentDB isolation identifiers with identifiers created by TencentDB MemoryCore's official v3 metadata APIs, then install the official MemoryPanel as a loopback-only management UI.

The change must preserve the previously approved runtime behavior:

- Codex, Pi, and the Owner's Hermes direct messages share personal L1/L2/L3.
- Every non-Owner Hermes direct-message principal is isolated and receives only L1 at runtime.
- Every Feishu group is isolated from every other group and receives only group L1 at runtime.
- Topics in one group have distinct Session IDs but share that group's Agent and L1.
- Group memory never imports a member's personal profile.
- Raw Feishu identifiers never enter TencentDB metadata, MLink config, logs, or exports.

Existing memory under the current `personal / keliang-personal / usr_owner_keliang` scope will not be migrated. It remains physically present but inactive after cutover.

## 2. Non-Goals

- Do not deploy or configure TencentDB MemoryProxy on port 8096.
- Do not change Codex, Pi, or Hermes model providers, model URLs, subscriptions, or authentication.
- Do not deploy Knowledge Service, Wiki, or Code Graph in this phase.
- Do not migrate, delete, rewrite, or merge the existing MLink MemoryCore data.
- Do not import HyMemory records.
- Do not make MemoryPanel reachable outside `127.0.0.1`.
- Do not customize TencentDB MemoryPanel to aggregate arbitrary legacy scopes.
- Do not promise physical absence of L2/L3 for non-Owner Agents; the official Core does not support disabling those layers per Agent.

## 3. Official Components and Versions

The implementation uses the official TencentCloud repository checkout at pinned commit `a5dcbe6`:

- MemoryCore v3 metadata APIs on the existing Gateway at `http://127.0.0.1:8420`.
- MemoryPanel single-service Docker build from `MemoryPanel/docker/local/Dockerfile.local`.
- Panel container port `8123`, published as `127.0.0.1:8125` on the host.

The image must be built from the pinned source commit rather than an unpinned `latest` tag. The public UI dependencies in the pinned checkout must build successfully before any metadata mutation.

## 4. Control-Plane Identity Model

### 4.1 Administrative bootstrap

On an empty MemoryCore metadata database, MLink performs the official bootstrap sequence:

1. Call `/v3/internal/meta/user/init-admin` once.
2. Store the returned system-admin `user_key` in macOS Keychain.
3. Use the admin identity only for user lifecycle and recovery operations.
4. Create a normal Owner User through `/v3/meta/user/create`.
5. Store the normal Owner User's default `user_key` in macOS Keychain.
6. Use the normal Owner identity for Team, Agent, Asset, and Panel operations.

The default Team and Agent created as an `init-admin` side effect remain unused. MLink must not run business memory under the system-admin identity.

If metadata is already initialized, MLink must not call `init-admin`. It must reuse validated Keychain credentials or stop with an actionable credential-recovery error.

### 4.2 Core-generated hierarchy

The normal Owner User creates:

- one Core-generated Owner Team;
- one Core-generated Owner Agent;
- the official auto-minted `chat_memory` Asset bound to that Agent.

MLink stores only the identifiers returned by Core:

```text
owner_user_id
owner_team_id
owner_agent_id
owner_chat_memory_asset_id
```

The previous deterministic values are no longer active routing identifiers.

### 4.3 Dynamic private and group Agents

MLink treats the three approved memory categories as routing policies, not three fixed physical Agents:

```text
owner policy
  -> one Core-generated Owner Agent

hermes-private policy
  -> one Core-generated Agent per external DM principal

hermes-groups policy
  -> one Core-generated Agent per Feishu group
```

Every dynamic Agent is owned by the normal Owner User and belongs to the Owner Team. Data-plane calls use the normal Owner User ID and the dynamic Agent ID. Isolation therefore resides in the Core-generated Agent ID.

Agent metadata contains only:

```json
{
  "mlink": {
    "installation_id": "<non-secret stable installation id>",
    "route_kind": "hermes-private | hermes-group",
    "principal_fingerprint": "<HMAC fingerprint>",
    "schema_version": 1
  }
}
```

The fingerprint is computed with MLink's Keychain-backed identity key. Raw Feishu `union_id`, `user_id`, `open_id`, `chat_id`, and topic IDs are forbidden in metadata.

Display names may be used only as mutable labels. Identity resolution never depends on display names.

## 5. Runtime Routing

### 5.1 Owner

Codex, Pi, and an Owner-bound Hermes DM route to:

```text
team_id  = owner_team_id
agent_id = owner_agent_id
user_id  = owner_user_id
```

Recall includes L1, L2, and L3.

Owner Feishu aliases remain Keychain-backed bindings to the same MLink Owner Principal. Changing a Feishu alias does not create a new TencentDB Agent.

### 5.2 Other direct messages

For a non-Owner Feishu DM:

1. Compute the external-principal HMAC fingerprint.
2. Resolve the fingerprint in the local principal-to-Agent mapping.
3. If absent, provision one Core Agent and its auto-minted Chat Memory Asset.
4. Persist the returned IDs before capture.
5. Route all later turns for that principal to the same Agent.

Runtime recall requests always set `include_agent_shared=false`, so MLink returns L1 only. Core may internally generate L2/L3, but MLink never recalls or injects those layers for the DM route.

### 5.3 Groups and topics

For a Feishu group:

1. Compute a group HMAC fingerprint from the stable group ID.
2. Resolve or provision one Core Agent for that group.
3. Route all group members to the same Agent.
4. Derive distinct Session IDs for distinct topics.

The group stores L0 and derived L1. Runtime recall returns group L1 only. No member's Owner/private Agent memory is consulted.

### 5.4 Provisioning failure

Dynamic provisioning is fail-closed:

- no mapping means no capture and no recall for that turn;
- never fall back to the Owner Agent, a shared private Agent, or a shared group Agent;
- return a bounded warning to Hermes without blocking the business response;
- retry on the next turn after checking Core metadata for an already-created matching Agent.

## 6. Mapping Persistence and Reconciliation

Add a Journal table conceptually equivalent to:

```text
principal_agents
  principal_fingerprint  UNIQUE
  route_kind
  backend_user_id
  backend_team_id
  backend_agent_id
  backend_asset_id
  display_label
  state
  created_at
  updated_at
```

No raw external ID is stored.

Provisioning uses an in-process single-flight lock keyed by fingerprint. Recovery handles the create-then-local-write failure window:

1. Query the Owner's Core Agents.
2. Inspect their `metadata_json` MLink marker.
3. Reuse the exact matching Agent and Asset.
4. Reconstruct the local mapping.

The same reconciliation runs during restore and identity-bundle import. Core remains the source of truth for generated IDs; the local mapping is a routing cache plus audit ledger.

## 7. CLI and TUI Flow

The installer becomes a two-stage approved workflow because generated Team and Agent IDs are unknown before Core mutation.

### Stage A: control-plane provisioning

Preview shows intent, endpoints, resource types, compensation rules, and protected invariants. After explicit confirmation it:

- validates the existing Gateway and its Bearer credential;
- initializes or reuses metadata administration;
- creates or reuses the normal Owner User;
- creates or reuses the Owner Team and Owner Agent;
- builds and starts the Panel-only container;
- records every returned ID and owned resource.

Stage A does not alter active MLink routing. If the user stops here, current memory behavior is unchanged.

### Stage B: routing cutover

After Stage A returns concrete IDs, MLink generates a new exact ChangeSet. A fresh confirmation is required before it:

- backs up the active MLink and Hermes configuration;
- replaces active personal routing with the returned IDs;
- enables dynamic private/group Agent provisioning;
- restarts the MLink Broker;
- restarts Hermes Gateway;
- runs Doctor and Panel visibility checks.

Stage B explicitly states that legacy memory is not migrated and will no longer be recalled.

### Management commands

The first release adds:

```text
mlink panel status
mlink panel open
mlink panel doctor
```

`panel open` opens `http://127.0.0.1:8125`. The normal Owner `user_key` is never printed. An explicit TUI action may copy it from Keychain to the clipboard; the UI must warn that clipboard managers can retain secrets.

## 8. Panel Deployment

Container identity:

```text
name:  mlink-memory-panel
image: mlink-memory-panel:a5dcbe6
bind:  127.0.0.1:8125 -> 8123/tcp
```

The Panel instance registry is rendered to:

```text
~/.mlink/panel/metadata-instances.json
```

with mode `0600` and mounted read-only. It contains:

- instance ID `default`;
- gateway endpoint `http://host.docker.internal:8420`;
- the Gateway Bearer required by the official Panel server.

This file duplicates the Gateway Bearer outside Keychain because the official Panel requires a server-side JSON instance registry. It must never enter Git, logs, image layers, preview output, or backups without encryption.

Panel environment:

```text
UI_DIST_DIR=./web/dist
METADATA_INSTANCES_CONFIG=/app/config/metadata-instances.json
KNOWLEDGE_LLM_BINDING_SYNC=false
LOG_LEVEL=info
LOG_FORMAT=json
```

No 8096 Proxy address or model credential is configured. Knowledge tabs are outside this phase and may show unavailable state; Chat Memory and metadata management must remain functional.

## 9. Configuration Model

Schema v3 adds a TencentDB control-plane block and separates policy IDs from physical backend IDs:

```yaml
schema_version: 3
control_plane:
  provider_id: dev.mlink.tencentdb
  instance_id: default
  panel_url: http://127.0.0.1:8125
  owner_user_id: <Core-generated>
  owner_team_id: <Core-generated>
  owner_agent_id: <Core-generated>
  owner_asset_id: <Core-generated>

routing_policies:
  owner:
    layers: [L1, L2, L3]
  hermes_private:
    layers: [L1]
    agent_policy: dynamic_per_principal
  hermes_groups:
    layers: [L1]
    agent_policy: dynamic_per_group
    session_policy: per_topic
```

Secrets remain Keychain references. Generated identifiers are non-secret but must remain consistent across config, Journal, Core metadata, and identity exports.

## 10. Security and Authorization

- Bind Panel only to host loopback.
- Use the system-admin key only for user-management operations.
- Use the normal Owner key for Team/Agent/Asset operations and Panel login.
- Keep Gateway Bearer, admin key, Owner key, identity HMAC key, and Hermes Broker grant separate.
- Never put a secret in argv, command logs, Docker image layers, or ChangeSet JSON.
- Redact Core-generated IDs to suffixes in ordinary TUI screens; full IDs may appear only in explicit diagnostic JSON.
- Never send raw Feishu IDs to Core metadata.
- Validate Agent ownership and Team membership after every create/reuse response.

## 11. Rollback and Uninstall

Stage A records which metadata entities and container resources it created. Before Stage B, compensating deletion is permitted only for unused resources created by the same unfinished plan.

After Stage B or after any memory has been written:

- uninstall stops and removes the Panel container and local registry;
- local routing may be restored from its exact backup;
- generated Core User/Team/Agent/Asset metadata is retained by default;
- deleting backend metadata requires a separate destructive plan showing affected memory counts;
- no uninstall path deletes MemoryCore L0-L3 implicitly.

If cutover fails, restore local config and restart Broker/Hermes. Newly created backend metadata remains inactive unless it is provably unused and the user approves compensation.

## 12. Quotas and Concurrency

Before creating an Agent, MLink checks Core instance quota. A quota failure blocks memory only for the new principal and does not affect existing mappings.

Concurrent first messages for the same principal must result in one active mapping. Tests cover:

- local single-flight behavior;
- Core create succeeds but local write fails;
- process restart between remote create and local commit;
- duplicate metadata markers;
- quota exhaustion;
- backend timeout and retry.

## 13. Verification Matrix

### Installation

- Panel UI and `/health` reachable only through `127.0.0.1:8125`.
- Panel container reaches `host.docker.internal:8420`.
- No listener on host port 8096 is added.
- Codex, Pi, and Hermes model/auth hashes remain unchanged.
- Admin and normal Owner credentials are distinct and stored correctly.

### Owner

- Codex, Pi, and Owner Hermes resolve the same Core-generated User/Team/Agent.
- Owner L0/L1/L2/L3 are visible in Panel.
- Owner alias rotation does not create another Agent.

### Private users

- two external users create two different Agents and Assets;
- repeated turns reuse the same generated Agent;
- neither user recalls the Owner or the other user;
- runtime bundles contain L1 only;
- raw Feishu IDs are absent from Core metadata, config, Journal, logs, and exports.

### Groups and topics

- two groups create two different Agents;
- two topics in one group share Agent/L1 and have different Sessions;
- group runtime bundles contain L1 only;
- group turns never include personal Owner/private memory.

### Failure and recovery

- Stage A cancellation leaves current routing unchanged.
- Stage B failure restores exact local hashes.
- orphan remote Agent reconciliation is idempotent.
- Panel restart preserves metadata visibility.
- Broker and Hermes restart preserve dynamic mappings.
- legacy `personal / keliang-personal` memory is not recalled after cutover.

## 14. Acceptance Criteria

The change is accepted only when:

1. all active User, Team, Agent, and Asset routing IDs originate from Core metadata responses;
2. the official Panel displays Owner, private-principal, and group Agents as separate assets;
3. Owner runtime recall includes L1/L2/L3;
4. private and group runtime recall includes L1 only;
5. group topics share Agent memory but not Session context;
6. no raw Feishu identifier reaches TencentDB metadata;
7. no LLM Proxy or model configuration change occurs;
8. no existing memory is migrated or deleted;
9. provisioning, cutover, rollback, and restore are independently reviewable;
10. all offline, adversarial, real Keychain, live MemoryCore, Panel, and restart tests pass.

## 15. Official References

- `MemoryPanel/README.md` at pinned official commit `a5dcbe6`.
- `MemoryPanel/docker/README.md` at pinned official commit `a5dcbe6`.
- `MemoryPanel/panel-api-doc.md` for Chat Memory L0-L3 management.
- `MemoryCore/src/metadata/router/v3-meta-schemas.ts` for official metadata creation contracts.
- `MemoryCore/src/metadata/service/metadata-service.ts` for admin bootstrap and automatic Chat Memory asset registration.
