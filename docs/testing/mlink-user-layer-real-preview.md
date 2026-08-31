# MLink Principal Routing Real-Machine Preview

Date: 2026-08-31

Status: previewed and reproducible; awaiting explicit approval; not applied

## Approval Identity

- Final ChangeSet: `plan_1e78ad2f7ca2232a424c44d69c`
- Invalidated ChangeSets: `plan_5b496cbe0ed3a1c09525934f9c`, `plan_3fccddff2c44b2e0b90fdfd41d`, `plan_840c3a08543ba520b2462228e6`
- Candidate path: `/tmp/mlink-principal-routing-preview`
- Candidate SHA-256: `bb7376875072da2f2e49cf128c15ce108556584b53e7d05bdd32005b9a0027e6`
- Git branch: `feat_product_design`
- Provider: `dev.mlink.tencentdb@0.1.0`
- MemoryCore endpoint: `http://127.0.0.1:8420`
- Selected Agents: Codex, Pi, Hermes
- Selected Connection: `local`

The existing MemoryCore credential and selected Feishu identity were supplied to each preview through the bounded `--install-secrets-stdin` JSON channel. Neither value was printed, placed in argv, written to the repository, or stored in Keychain.

## Owner Candidate

Hermes candidates were read from `/home/keliang/.hermes/state.db` using SQLite URI read-only mode. The explicit owner selection is:

| Display name | Kind | Redacted value | Last seen |
|---|---|---|---|
| 陈科良 | `union_id` | `…779a` | 2026-08-31 11:44 |

The older `user_id …3614` candidate was not selected. MLink stores the raw selected value only in the macOS Keychain account `identity/binding/owner-feishu-union-1` when an approved Apply occurs.

## MLink Schema and Routing

The proposed `~/.mlink/config.yaml` is Schema v2 and contains one stable Owner Principal plus three Memory Spaces:

| Item | Proposed behavior |
|---|---|
| `principal:owner` | stable canonical owner; independent of any Feishu ID |
| `binding:owner-feishu-union-1` | redacted `union_id` alias maps to Owner |
| `personal-owner` | Owner L1/L2/L3; shared by Codex, Pi, and Owner Hermes DM; `include_agent_shared=true` |
| `hermes-private` | isolated per-user Hermes DM L1 only; no L2/L3; `include_agent_shared=false` |
| `hermes-groups` | isolated per-group L1 only; topics share the group Principal; no member personal memory; `include_agent_shared=false` |
| Codex route | fixed `personal-owner` |
| Pi route | fixed `personal-owner` |
| Hermes route | dynamic Owner/private/group authorization route |

## Exact Proposed Operations

| # | Action | Target | Proposed SHA-256 / semantic result | Rollback |
|---:|---|---|---|---|
| 1 | create | `/Users/keliang/.local/bin/mlink` | `bb7376875072da2f2e49cf128c15ce108556584b53e7d05bdd32005b9a0027e6` | remove created file |
| 2 | create | `/Users/keliang/.mlink/config.yaml` | `9fde5087c73671c5403ed736233ed8d509c80bfddb912392ab0f6f5a0b6c0161`; Schema v2 routing above | remove created file |
| 3 | create | `/Users/keliang/.codex/hooks.json` | MLink lifecycle Hooks; existing Hooks preserved | remove created file |
| 4 | create | `/Users/keliang/.pi/agent/extensions/mlink.ts` | official Pi lifecycle Extension | remove created file |
| 5 | create | `/home/keliang/.hermes/plugins/mlink/plugin.yaml` | Hermes user Provider manifest | remove created file |
| 6 | create | `/home/keliang/.hermes/plugins/mlink/__init__.py` | Hermes `MemoryProvider` bridge | remove created file |
| 7 | create | `/home/keliang/.hermes/mlink.json` | private delegated Broker grant | remove created file |
| 8 | semantic merge | `/home/keliang/.hermes/config.yaml` | before `dc88cec33f11785e4605dd2bbbfe7c38d37d07f9aa93e63587b3ba039788995b`; proposed `ce5d0d530ca10010b5df2440211a3dd4f5dcb3065d17bff2575484eb62950f8d` | restore backup |
| 9 | create | `/Users/keliang/Library/LaunchAgents/dev.mlink.broker.plist` | MLink Broker user service | remove created file |
| 10 | service action | `service:bootstrap:dev.mlink.broker` | bootstrap LaunchAgent | compensating service action |
| 11 | service action | `service:kickstart:dev.mlink.broker` | start Broker | compensating service action |

An approved Apply also writes these four secrets transactionally to macOS Keychain service `dev.mlink`: `connection/local/token`, `identity/hmac-key`, `adapter/hermes/token`, and `identity/binding/owner-feishu-union-1`. Any later resource failure restores their prior state.

## Exact Hermes Semantic Merge

The current real Hermes values and proposed owned values are:

| Path | Current | Proposed |
|---|---|---|
| `group_sessions_per_user` | explicit `true` | explicit `false` |
| `thread_sessions_per_user` | absent, Hermes default `false` | explicit `false` |
| `memory.memory_enabled` | `true` | `false` |
| `memory.user_profile_enabled` | `true` | `false` |
| `memory.provider` | `hy-memory` | `mlink` |
| `agent.disabled_toolsets[memory]` | absent | present |

Hermes model, provider, Base URL, authentication, gateway, and every unowned setting are covered by `hermes.model_auth_and_unowned_config`. Its before and proposed hash is `06c3e152f2caac987c60076ec9ed87de522c6e2945ba7f966cefb197024d3ee3`; `preserved=true`.

## Reproducibility and No-Write Proof

Two independent final preview processes returned the same `plan_1e78ad2f7ca2232a424c44d69c`. A controlled preview kept these protected Agent configuration hashes identical before and after:

| Protected state | SHA-256 | Result |
|---|---|---|
| Codex `config.toml` | `3ac101d346cc3a6e35825a0a6b8e4b224c1ff050faab3ba3cb1a87b44ba8b13f` | unchanged |
| Pi `settings.json` | `da7ec5c4f3e914491b1177093756e8bf36d29084817c318f6b47fc43da1bc8dd` | unchanged |
| Hermes `config.yaml` | `dc88cec33f11785e4605dd2bbbfe7c38d37d07f9aa93e63587b3ba039788995b` | unchanged |

Codex had independently changed its config hash from an earlier `b618…` observation to `3ac1…` before the controlled final window. MLink neither reads nor targets `config.toml`; the final before/after hash and mtime prove the preview did not cause that external change.

After the final preview:

- the installed MLink binary, MLink config, Unix socket, Codex Hook, Pi Extension, LaunchAgent plist, Hermes Provider files, and Hermes grant file were all absent;
- `launchctl` had no `dev.mlink.broker` service;
- all four proposed `dev.mlink` Keychain accounts returned macOS Security item-not-found exit code `44`;
- the failed `plan_840c…` attempt created `~/.mlink/journal.db`, but `owned_resources`, `adapter_installations`, `backup_artifacts`, and `journal_events` all contained zero rows;
- no encrypted `*.mlink` identity bundle existed under `~/.mlink`;
- no Agent, LaunchAgent, Keychain, OrbStack file, or MemoryCore state was changed.

## Failed Apply Remediation

The first user-driven Apply of `plan_840c3a08543ba520b2462228e6` exposed that macOS `security add-generic-password -w` opens the controlling terminal and requests the secret twice instead of consuming ordinary process stdin. The attempt stopped before any managed file or formal Keychain item was written. The obsolete TUI process was terminated after its exact command was verified.

Commit `0876ecd` replaces ordinary stdin with a bounded protected pseudo-terminal flow. The secret is sent twice only after each prompt is detected, never appears in argv or logs, and the process has a 15-second timeout. The same fix also:

- trims pasted Token whitespace before planning;
- clears confirmation and returns to credential entry after any Apply failure;
- prevents a second Enter from masking the original error with `MemoryCore token input is required`.

A real macOS integration test wrote and read a uniquely named temporary Keychain item, deleted it, and confirmed that no `dev.mlink.integration*` item remained. The four formal `dev.mlink` accounts remain absent.

## Verification

- `go test ./...`: pass
- `go test -shuffle=on -count=2 ./...`: pass
- `go vet ./...`: pass
- `GOTOOLCHAIN=go1.22.12 CGO_ENABLED=0 go test ./...`: pass
- real temporary-login-Keychain PTY write/read/delete integration: pass
- the install-preview disclosure regression test first failed against the incomplete output, then passed after Schema, Principal, Binding, Space, and Agent-route diffs were added

No Apply is authorized by this report. Applying requires a fresh explicit confirmation of `plan_1e78ad2f7ca2232a424c44d69c` after this exact ChangeSet is shown.
