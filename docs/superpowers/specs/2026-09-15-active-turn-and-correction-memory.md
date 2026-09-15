# Active-turn archive protection and priority corrections

Approved scope: idle Skill archive after one hour; suspend archive when a user
message arrives, restart the interval after the completed turn is captured;
submit explicit user corrections immediately with preceding context and links
to potentially superseded Skills. Keep model/API/auth settings untouched.

## Contract review

- Reuse authenticated `/v1/turn-fragments` for early user capture. Existing
  Codex and Cursor hooks already emit it. Hermes and Pi must emit the same
  canonical fragment before model work. Completed turns remain the ordinary
  capture/Skill-buffer boundary; do not invent assistant messages.
- Add an optional negotiated `observe_user_turn` Provider capability, without
  changing required interfaces or the protocol version. The Broker supplies
  canonical identity, the user message and bounded earlier session messages.
  Providers own archive and correction behavior. Older providers remain usable.
- Persist observation receipts in the journal: active/queued/failed and Core
  task reference plus related Skill IDs/versions. `queued` never means updated.
  Repeated fragments must not launch the correction task twice.
- The TencentDB lifecycle invalidates old timer generations on observation.
  A completed capture clears only its matching active turn; other concurrent
  turns keep the session paused. Provider shutdown must not archive active
  sessions. SessionEnd remains an explicit finalization boundary.
- Explicit correction detection is conservative, based on the direct user
  message, not recalled text or tool output. Corrections append to the original Skill buffer and immediately force-archive it
  with prior messages, verbatim correction and relevant Skill ID/version
  references. Native Skill updates retain version lineage. This consumes the stale buffer together with the correction, preventing a later
  idle archive from reapplying the old conclusions. The API submission
  is immediate; model extraction remains asynchronous and is reported as such.
- Failure to submit a correction is recorded, not represented as success.
  The completed turn still reaches the ordinary Skill path. Unknown-result
  writes must not be replayed automatically.

## Acceptance evidence

1. Advancing a fake timer beyond one hour while a turn is active never archives.
2. Completion starts a fresh one-hour interval; stale callbacks and completion
   of an earlier turn cannot archive a newer active turn.
3. Explicit correction reaches extract before assistant completion, carries
   prior context and related Skill references, and returns an auditable task.
4. Normal prompts do not trigger correction extraction; identities stay scoped.
5. Duplicate user fragments are idempotent, restart keeps activity protection,
   and existing capture/recall/Skill archive tests remain green.
6. Verify the installed Hermes adapter and a real recall of the corrected rule;
   distinguish Core acceptance from an actually updated Skill version.
