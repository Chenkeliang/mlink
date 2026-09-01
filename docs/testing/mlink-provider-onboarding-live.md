# MLink Provider Onboarding Live Acceptance

Date: 2026-09-01

## Scope

The acceptance test exercises a fresh TencentDB MemoryCore onboarding path with the official pinned image while refusing all production resource names and ports.

Verified sequence:

1. unique loopback port, container, network, data volume, service ID, and temporary filesystem;
2. official MemoryCore image install and compatible `/health` response;
3. headless metadata provisioning of distinct administrator and Owner identities;
4. Core-generated Owner User, Team, Agent, and Chat Memory Asset IDs persisted in Journal;
5. first writable MLink configuration emitted directly as Schema v3, with supplied custom legacy IDs removed;
6. capture and eventual recall under the Core-generated IDs;
7. later Memory Hub planning leaves all four persisted IDs byte-for-byte unchanged;
8. ordinary provider uninstall removes the owned container and config while retaining the data volume;
9. test-only container, network, and volume removed during final fixture cleanup.

The separate `TestLiveControlPlaneAndOfficialPanel` acceptance covers a real official Memory Hub container and Owner-key login. The provider-onboarding test intentionally does not bind the Hub to production ports `8125` or `8424`.

## Safety gates

`TestLiveOfficialProviderOnboarding` is skipped unless `MLINK_TEST_PROVIDER_ONBOARDING=1` is present. It refuses:

- `tdai-memory-core`;
- `tdai-memory-core-data`;
- `tdai-memory-stack`;
- host port `8420`;
- any cleanup target without the `mlink-e2e-` prefix.

The memory LLM key is supplied through the test process environment and a temporary mode-`0600` Docker env file. It is absent from Plan JSON, Docker argv, test logs, and this document.

## Verified execution

Official image:

```text
agentmemory/memory-core@sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11
```

Result:

```text
=== RUN   TestLiveOfficialProviderOnboarding
provider_plan=plan_d8a47a04e31e46848be69c5645
control_plan=plan_45db731882abee7642da4e86c3
install_plan=plan_f738b6f06a5aa5c9e3531d10ac
owner_user=…b69hxsn4
owner_team=…b6wwrtpo
owner_agent=…b6wn8e1j
owner_asset=…b6wn8e1j
--- PASS: TestLiveOfficialProviderOnboarding (21.94s)
```

The repeated run also passed in `26.75s`. A post-run Docker inventory contained no `mlink-e2e-` container, volume, or network.

Post-review verification after adding the fresh-Journal and existing-backend bootstrap guards also passed in `27.68s`:

```text
provider_plan=plan_2e9c9286aa00c20804f2a51038
control_plan=plan_a535882c87f867454d03e4f8e9
install_plan=plan_e6ff5f2f7c853e82d530963900
owner_user=…4nrtotxd
owner_team=…4njf52qs
owner_agent=…4nxho76g
owner_asset=…4nxho76g
```

## Re-run

Provide a compatible OpenAI-style memory LLM endpoint, model, and protected API key, then run:

```text
MLINK_TEST_PROVIDER_ONBOARDING=1 \
MLINK_TEST_LLM_BASE_URL=<https-or-loopback-url> \
MLINK_TEST_LLM_MODEL=<model> \
MLINK_TEST_LLM_API_KEY=<protected-key> \
go test ./internal/e2e -run '^TestLiveOfficialProviderOnboarding$' -count=1 -v -timeout=10m
```

Never paste the API key into issue reports or committed shell scripts.
