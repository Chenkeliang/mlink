# MLink Real-Machine Read-Only Preview

Date: 2026-08-28

Status: previewed; not applied

## Candidate

- Git branch: `feat_product_design`
- Candidate path: `/tmp/mlink-user-layer-preview`
- Candidate SHA-256: `e40373afdbb78ccd3d55bc8694d2155af6ec320c6612e79db71b1ad6df468f9b`
- ChangeSet: `plan_5b496cbe0ed3a1c09525934f9c`
- Provider: `dev.mlink.tencentdb@0.1.0`
- MemoryCore endpoint: `http://127.0.0.1:8420`
- Hermes target: `hermes-agent-env`, Hermes Agent `v0.20.5`
- Auto-detected private Orb bridge: `192.168.139.3:8097`

The existing MemoryCore Gateway credential was supplied to the preview through stdin. It was not printed, placed in argv, written to the repository, or stored in Keychain.

## Proposed ChangeSet

| Action | Target | Owned change |
|---|---|---|
| create | `/Users/keliang/.local/bin/mlink` | install the verified candidate binary |
| create | `/Users/keliang/.mlink/config.yaml` | create the non-secret TencentDB Connection with Keychain references |
| create | `/Users/keliang/.codex/hooks.json` | add official Codex lifecycle Hooks |
| create | `/Users/keliang/.pi/agent/extensions/mlink.ts` | add the Pi lifecycle Extension |
| create | `/home/keliang/.hermes/plugins/mlink/plugin.yaml` | add the Hermes user Provider manifest |
| create | `/home/keliang/.hermes/plugins/mlink/__init__.py` | add the Hermes MemoryProvider bridge |
| create | `/home/keliang/.hermes/mlink.json` | add the private delegated Broker grant |
| semantic merge | `/home/keliang/.hermes/config.yaml` | select `mlink`, disable built-in memory injection and user profile, and disable the built-in `memory` toolset |
| create | `/Users/keliang/Library/LaunchAgents/dev.mlink.broker.plist` | install the user Broker service |
| service | `dev.mlink.broker` | bootstrap and kick-start the user LaunchAgent |

The Hermes semantic merge changes only these owned values:

- `memory.memory_enabled`: configured value → `false`
- `memory.user_profile_enabled`: configured value → `false`
- `memory.provider`: existing Provider → `mlink`
- `agent.disabled_toolsets[memory]`: absent/preserved → present

Hermes model, model-provider, model Base URL, authentication, gateway, and all other unowned configuration are protected by the invariant `hermes.model_auth_and_unowned_config`. Its before and proposed hashes are both `392f67d6cc66d84e7472deeb8e6680f73a2a760c0a04191d2740113b1f03a194`; `preserved=true`.

## No-Write Proof

Before and after the final preview:

- Codex model configuration SHA-256 remained `b61810bf92641faa1329eba34b598c121571a9438745f1edfa0806b303b28732`.
- Pi settings SHA-256 remained `da7ec5c4f3e914491b1177093756e8bf36d29084817c318f6b47fc43da1bc8dd`.
- Hermes configuration SHA-256 remained `dc88cec33f11785e4605dd2bbbfe7c38d37d07f9aa93e63587b3ba039788995b`.
- No Codex Hook file, Pi MLink Extension, installed MLink binary, LaunchAgent plist, MLink Journal, Hermes MLink Provider, or Hermes grant file existed.
- All three `dev.mlink` Keychain accounts returned macOS Security item-not-found code `44`.
- Re-running the preview produced the same ChangeSet ID.

No Agent, LaunchAgent, Keychain, OrbStack file, or MemoryCore state was changed by the preview.
