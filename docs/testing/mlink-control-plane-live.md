# MLink TencentDB Control Plane — Isolated Live Acceptance

Date: 2026-08-31

## Isolation

- Core endpoint: `http://127.0.0.1:8420` (`mlink-memorycore-test:v2.0.1`)
- Service IDs: `mlink-control-e2e-20260831a`, `mlink-control-e2e-20260831b`
- Panel test port: `127.0.0.1:18125` (production port `8125` was refused)
- Panel test container name derived from the unique Service ID (production name was refused)
- Active Service ID `default`, current MLink config, Agent hooks, Hermes config, and current memory were not changed

Both unique test instances were destroyed through `/v3/instance/destroy` in test cleanup. No test Panel container or port listener remains.

## Passed

- isolated Core `system_admin` initialization;
- separate normal Owner creation and distinct credentials;
- Owner Team, Agent, and auto-minted Chat Memory Asset;
- dynamic private Agent creation from a fingerprint-only marker;
- raw Feishu IDs absent from created metadata;
- cleanup of the unique test instance;
- strict client updated to the official Asset response fields `last_used_at` and `usage_count`.

## Superseded Panel-only build result

The exact official release source at `a5dcbe6` fails while building `MemoryPanel/docker/local/Dockerfile.local`. The Dockerfile replaces Debian's source with `mirrors.tencent.com` before installing `ca-certificates`; the mirror redirects to HTTPS and the base image has no trusted CA bundle yet. `apt-get update` therefore fails certificate verification and cannot install the packages.

The official remote HEAD checked in a disposable clone at `3efcd31` contains the same Dockerfile sequence, so the issue is not fixed upstream as of this test. MLink did not patch the official source, use a derived Dockerfile, start MemoryProxy/Knowledge Service, or change any model configuration.

The user subsequently selected Tencent's official prebuilt `agentmemory/memory-hub` image. MLink now pins the official arm64 manifest digest and does not build or patch this Dockerfile. A new isolated Hub acceptance must verify Panel, Knowledge, and Owner login before real Stage A.

## Official Memory Hub acceptance — passed

On 2026-09-01 the guarded live test used:

- Service ID `mlink-control-e2e-20260901a`;
- Panel port `18125` and Knowledge port `18424`;
- unique container and Knowledge volume;
- official arm64 image digest `sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104`.

Passed in 104.69 seconds:

- isolated admin and normal Owner creation;
- Owner Team, Agent, and Chat Memory Asset;
- dynamic principal Agent with fingerprint-only metadata;
- Panel health, public instance listing, and Owner login;
- Knowledge health;
- no port 8096 listener and no Agent model configuration change.

The unique Core Service ID, container, volume, and listeners were removed after the test. A cleanup-order defect discovered by the post-test audit was corrected so future tests remove the container before its attached volume.
