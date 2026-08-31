# MLink Principal Routing Live MemoryCore Acceptance

Date: 2026-08-31

Status: passed against the isolated local MemoryCore instance.

## Exact write scope

```text
container: mlink-memorycore-test
image: mlink-memorycore-test:v2.0.1
base_url: http://127.0.0.1:8420
service_id: mlink-alias-20260831-140336
team_id: team-mlink-alias-20260831-140336
owner agent_id: owner-mlink-alias-20260831-140336
private agent_id: private-mlink-alias-20260831-140336
group agent_id: groups-mlink-alias-20260831-140336
owner user_id: usr-owner-mlink-alias-20260831-140336
```

The existing Gateway Token was read from the container environment into the test process. It was not printed, committed, or placed in a repository file. All Canary text and external aliases were generated solely for this isolated scope. The records remain in the isolated test volume and do not overlap a normal user identity.

## Root-cause audit of the first probe

The first probe failed because its English text described exchanging a test marker rather than asserting a durable user/group fact. Evidence showed:

- L0 accepted and stored all 10 messages.
- The L1 task was enqueued and completed normally in about 8.7 seconds.
- The extraction checkpoint recorded `extracted=0`.
- Pipeline status returned idle with no queued/running task.
- No capture was automatically replayed.

The only changed test variable was the semantic statement. The passing probe used an explicit durable personal alias and an explicit durable group agreement, matching the previously characterized MemoryCore extraction contract. No MLink Router, Provider, identity, retry, or timeout behavior was changed.

## Mandatory identity-continuity gates

| Gate | Result |
|---|---|
| Seed owner memory through the old Feishu alias | Pass |
| Recall through old alias, Codex, and Pi | Pass |
| Add a new alias to the same owner | Pass |
| Recall through the new alias without data migration | Pass |
| Close and reconstruct a Broker HTTP instance from restored identity state | Pass |
| Revoke old alias while keeping new alias active | Pass |
| Unbound DM cannot recall owner Canary | Pass |
| Group Principal cannot recall owner Canary | Pass |
| Encrypted export/decrypt/reconstruction preserves canonical owner | Pass |
| Derived old/new unbound users contain no copied owner Canary | Pass |

The final owner alias test, including real MemoryCore L1 extraction and two Broker HTTP constructions, passed in 20.53 seconds.

## Group and topic gates

| Gate | Result |
|---|---|
| Capture a durable group agreement into group L1 | Pass |
| A second topic in the same group recalls the group Canary | Pass |
| The two topics use different Session IDs | Pass |
| The two topics use the same group canonical user | Pass |
| A different group cannot recall the Canary | Pass |
| Owner personal Space cannot recall the group Canary | Pass |
| Unbound private DM cannot recall the group Canary | Pass |
| Group/private Spaces keep `include_agent_shared=false` | Pass |
| Owner Space keeps `include_agent_shared=true` | Pass |

The group/topic test passed in 20.21 seconds.

## Request and storage observations

- Every bound old/new alias request sent the exact same owner canonical `user_id` to MemoryCore.
- No Feishu alias, chat ID, or topic ID was sent as a Provider identity.
- Alias rotation changed only Broker Binding resolution; no MemoryCore copy or migration request occurred.
- Revocation stopped old-alias memory access before Provider invocation.
- Captures remained `accepted` and `replay_safe=false`; the tests never retried a maybe-sent write.
- Owner, unbound DM, group A, and group B used distinct effective identity dimensions.

## Command result

Final integration command shape:

```text
go test -tags=integration ./internal/e2e \
  -run 'TestLive(AliasRotationWithoutMemoryMigration|GroupTopicsShareGroupMemoryAndRemainSeparatedFromOwner)' \
  -count=1 -v -timeout=8m
```

Final result: both tests passed. This validates the user-approved identity continuity and group-space rules against the isolated MemoryCore backend. It does not authorize installation into real Codex, Pi, Hermes, Keychain, or LaunchAgent state; the next step remains a new read-only real-machine ChangeSet.
