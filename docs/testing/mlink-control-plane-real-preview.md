# MLink Real-Machine Stage A Preview

Date: 2026-08-31

Status: **previewed, not applied**. Stage A is blocked because the unchanged official Panel Dockerfile cannot build on this host; see `mlink-control-plane-live.md`.

## Candidate

- Branch: `feat_product_design`
- Candidate: `/tmp/mlink-control-plane-preview`
- Candidate SHA-256: `7c8a3887a03f5c9aa52b1f91291eb546a22d1d97c3277261e1e44cab432d138f`
- Installed MLink SHA-256 before/after preview: `bbda95d1b3c3bbfdf45e71ddae6c5771942807b9065e3d564b22b2830c887256`
- Official Panel source: clean checkout at `a5dcbe6`
- Planned image: `mlink-memory-panel:a5dcbe6`

## Exact Stage A ChangeSet

- Plan ID (three consecutive previews): `plan_99e293816f1d956726df0a56db`
- Selected instance: `default`
- Full JSON preview: `/tmp/mlink-stage-a-preview.json` (local ephemeral evidence)

Operations:

1. `remote:tencentdb:default:create:system-admin`
2. `remote:tencentdb:default:create:owner-user`
3. `remote:tencentdb:default:create:owner-team`
4. `remote:tencentdb:default:create:owner-agent`
5. create protected `~/.mlink/panel/metadata-instances.json` at mode `0600` during Apply only
6. build `mlink-memory-panel:a5dcbe6` from the pinned official Dockerfile
7. run `mlink-memory-panel` at `127.0.0.1:8125:8123`

The registry content and Gateway Bearer are absent from the ChangeSet. Stage A contains no active MLink config operation, no Broker/Hermes restart, no MemoryProxy/8096 operation, and no model/auth operation.

## Zero-Write Proof

Hashes were identical immediately before and after another preview:

| Resource | SHA-256 |
|---|---|
| `~/.mlink/config.yaml` | `9fde5087c73671c5403ed736233ed8d509c80bfddb912392ab0f6f5a0b6c0161` |
| `~/.codex/hooks.json` | `a2f139c141781fdc9467e10ee603288b8903fbb17045e266feb34846b991a9bd` |
| Pi extension | `004055a36522fd6417d80c4abedcba3aa30489ff4d5510960bb063b6143b99a4` |
| Hermes `config.yaml` | `ce5d0d530ca10010b5df2440211a3dd4f5dcb3065d17bff2575484eb62950f8d` |
| Hermes `mlink.json` | `3ce380cb387da5ed3083be2f69a081a294802f5261f2cfa1b337030aa2845cb3` |
| Hermes plugin manifest | `65bd2e99d6df2ffaf53ca076e11e645a7aa9d16b9aee667615dd508a91808673` |
| Hermes plugin code | `64c876cf72e76b558c15f0816cf42a47576d7bb0d05c087508ac80e0d4d091d9` |

Additional checks after preview:

- Journal migrations remain `1,2,3`; preview did not create v4 tables.
- Admin and Owner Keychain accounts are absent.
- Panel registry is absent.
- No `mlink-memory-panel` image or container exists.
- No listener exists on port `8125`.
- The installed binary was not replaced.

## Gate

Do not approve or apply this Stage A Plan yet. The exact official Panel build fails before image creation because of the upstream apt/CA ordering issue. A fresh Stage A Plan must be generated after choosing one of these separately authorized paths:

1. Tencent publishes a fixed official Dockerfile or prebuilt Panel-only image; or
2. the user explicitly authorizes a derived Dockerfile workaround, which is currently out of scope because the requested policy is “no compatibility modification.”

Stage B has not been previewed against real generated IDs and cannot be applied before Stage A succeeds.
