# MLink Local Cursor Acceptance

Date: 2026-09-01

## Scope

This acceptance covers local Cursor Desktop/CLI user Hooks and the global stdio MCP server. Cursor Cloud Agent and Tab memory are outside this adapter.

Official contracts used:

- Cursor Hooks: `https://cursor.com/docs/hooks`
- Cursor MCP: `https://cursor.com/docs/context/model-context-protocol`

## Applied ChangeSet

`plan_0deee68e47274b26507bb57a73` changed only:

- MLink Schema v3 config: added the fixed Owner `cursor` adapter;
- `~/.cursor/hooks.json`: added fail-open `sessionStart`, `beforeSubmitPrompt`, `afterAgentResponse`, and `sessionEnd` commands;
- `~/.cursor/mcp.json`: added the credential-free `mlink-memory` stdio server.

The `all_non_cursor_configuration` protected invariant was byte-semantically equal before and after. Installation-time SHA-256 checks confirmed that Cursor `argv.json`, `cli-config.json`, `acp-config.json`, and `ide_state.json` were unchanged. Existing non-MLink MCP servers and their configuration were preserved.

## Runtime Fixes Found During Acceptance

Two defects were reproduced and fixed with tests:

1. Enabling Cursor did not restart the Broker, so the running process retained the previous adapter registry.
2. The fixed Broker grant list enumerated Codex and Pi but omitted Cursor, returning HTTP 401 for the local Cursor adapter.

The final installed candidate is `0.1.0-cursor.2` at commit `ac5a281`, built with Go 1.27.0.

## Evidence

- Cursor CLI `agent mcp list-tools mlink-memory` discovered exactly:
  - `mlink_memory_search(query, limit)`
  - `mlink_memory_status()`
- The local Broker health endpoint returned HTTP 200 for `adapter_id=cursor` over the private Unix socket.
- An official-shape `beforeSubmitPrompt` + `afterAgentResponse` pair produced one Cursor Journal event, which reached `accepted` in MemoryCore.
- `mlink doctor cursor --json` reported Cursor Hooks active, Cursor MCP configured, Broker/Provider available, pinned Hub image, persistent Knowledge volume, and clean Journal queue.
- `mlink maintenance journal list --json` returned an empty unresolved list.

The Cursor CLI account itself was not logged in, so a model-driven CLI tool call was not attempted. This did not affect MCP process discovery or the already configured Cursor Desktop account.
