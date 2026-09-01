# MLink Real-Machine Official Memory Hub Stage A

Date: 2026-09-01

Status: **Stage A and Stage B applied and verified.**

## Candidate and exact plan

- Branch: `feat_product_design`
- Candidate: `/tmp/mlink-memory-hub-candidate`
- Candidate SHA-256: `b66fbaed49ac07ff3eebf1864242287221856f5fdf15b328b3bf5f5bab121103`
- Installed MLink remained unchanged: `bbda95d1b3c3bbfdf45e71ddae6c5771942807b9065e3d564b22b2830c887256`
- Official arm64 image: `agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104`
- Exact Stage A Plan (three consecutive previews): `plan_d3a4c14416b4627a1d33915c02`

The Plan contained eight operations:

1. Core system-admin intent;
2. normal Owner User intent;
3. Owner Team intent;
4. Owner Agent and auto-minted Chat Memory Asset intent;
5. protected `0600` instance registry;
6. digest-pinned official image pull;
7. labeled persistent `tdai-panel-data` volume;
8. `tdai-memory-hub` with Panel and Knowledge bound to host loopback.

No operation changed active MLink routing, Agent configuration, model providers, or legacy memory.

## Zero-write preview evidence

Before Apply:

- the active Journal contained migrations `1,2,3` only;
- control-plane Keychain accounts were absent;
- the production Hub container, volume, registry, and ports were absent;
- three previews returned the same Plan ID;
- config, Hook, Pi, installed binary, and Hermes hashes matched the values below.

## Applied resources

- container `tdai-memory-hub`: healthy;
- Panel: `127.0.0.1:8125`, healthy;
- Knowledge: `127.0.0.1:8424`, healthy;
- Memory instance `default`: visible in Panel;
- Knowledge volume: `tdai-panel-data`, label `dev.mlink.component=memory-hub`;
- registry: `~/.mlink/panel/metadata-instances.json`, mode `0600`;
- control-plane state: `provisioned`;
- generated ID suffixes: User `moi6r72v`, Team `mo0mljnc`, Agent `mo8s7iqg`;
- admin and Owner user keys: present and distinct;
- Owner login through Panel: HTTP 200, `valid=true`;
- Wiki assets: `0`;
- CodeGraph assets: `0`;
- image RepoDigest exactly matches the approved official digest;
- no listener on port 8096.

## Protected hash comparison

The following hashes were identical before and after Apply:

| Resource | SHA-256 |
|---|---|
| `~/.mlink/config.yaml` | `9fde5087c73671c5403ed736233ed8d509c80bfddb912392ab0f6f5a0b6c0161` |
| `~/.codex/hooks.json` | `a2f139c141781fdc9467e10ee603288b8903fbb17045e266feb34846b991a9bd` |
| Pi extension | `004055a36522fd6417d80c4abedcba3aa30489ff4d5510960bb063b6143b99a4` |
| Installed MLink | `bbda95d1b3c3bbfdf45e71ddae6c5771942807b9065e3d564b22b2830c887256` |
| Hermes `config.yaml` | `ce5d0d530ca10010b5df2440211a3dd4f5dcb3065d17bff2575484eb62950f8d` |
| Hermes `mlink.json` | `3ce380cb387da5ed3083be2f69a081a294802f5261f2cfa1b337030aa2845cb3` |
| Hermes plugin manifest | `65bd2e99d6df2ffaf53ca076e11e645a7aa9d16b9aee667615dd508a91808673` |
| Hermes plugin code | `64c876cf72e76b558c15f0816cf42a47576d7bb0d05c087508ac80e0d4d091d9` |

Journal migration `4` was added during Apply to persist generated control-plane state. No active configuration was cut over.

## Stage B cutover

The first cutover Plan correctly rolled back after its Hermes restart was accidentally routed to the macOS host instead of Orb. Evidence after failure showed the original binary hash, Schema v2 config hash, `provisioned` state, running old Broker, and unchanged Hermes process. No cutover ownership was committed.

Two safety fixes were then committed and tested:

- Stage B updates and backs up the MLink binary in the same transaction as Schema v3 config;
- Hermes restart is routed through the Orb target.

The final exact Plan `plan_2591dc31dd43b31e9db2ac271c` applied:

- installed binary SHA-256 `91c54302f8737185d2a4816dd3ffe77417828d0f8a5c26bafb7e8c26e81a568c`;
- Schema v3 config SHA-256 `b499b5a2852ea0fb6c6e43b927d86444792af236545bed4dcffde65f68783345`;
- Core-generated Owner User/Team/Agent/Asset;
- dynamic Agent limit `500`;
- Owner policy L1/L2/L3;
- private/group policies L1 only, with group topics split by Session;
- Broker PID refreshed and Hermes restarted at 2026-09-01 12:05:30 CST;
- control-plane state changed to `active`.

Post-cutover read-only probes:

- Codex local Recall: HTTP 200;
- Pi local Recall: HTTP 200;
- Owner Hermes Recall through the bridge: HTTP 200;
- Hub Panel, Knowledge, and instance visibility: healthy;
- no port 8096 listener;
- Codex Hook, Pi extension, and all Hermes protected hashes unchanged;
- dynamic principal mappings: `0` until the first non-Owner DM/group message;
- legacy memory remains physically present (`L0=132`, `L1=23`, `L2=7`) but is no longer active;
- new Owner memory starts empty and will be populated by subsequent turns.

Pi still reports `awaiting_first_turn`; Cursor is not part of the current Codex/Pi/Hermes adapter release. One pre-existing ambiguous Journal event remains for separate reconciliation.
