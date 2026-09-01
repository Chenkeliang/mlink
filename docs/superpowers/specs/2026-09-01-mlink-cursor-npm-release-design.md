# MLink Local Cursor Integration Design

Date: 2026-09-01

Status: approved for implementation

## Purpose

Add local Cursor Desktop/CLI memory without changing Cursor's model or subscription, and update project Agent instructions.

Build baseline: Go 1.27.0 managed by `g`. MCP uses official `github.com/modelcontextprotocol/go-sdk` v1.7.0.

Cursor Cloud Agent is explicitly out of scope because it cannot access the local Broker and does not load user-level hooks.

## Cursor integration

### Hooks

MLink semantically merges user-level `~/.cursor/hooks.json` and installs no runtime besides the native `mlink` binary.

Managed command hooks:

```text
sessionStart        -> mlink hook cursor sessionStart
beforeSubmitPrompt  -> mlink hook cursor beforeSubmitPrompt
afterAgentResponse  -> mlink hook cursor afterAgentResponse
```

Official Cursor input provides stable `conversation_id`, per-turn `generation_id`, workspace roots, prompt text, and assistant response text.

- `sessionStart` performs best-effort workspace-scoped Owner recall and returns bounded `additional_context`; it is not treated as a delivery guarantee because Cursor documents the hook as fire-and-forget.
- `beforeSubmitPrompt` records the user fragment keyed by conversation/generation.
- `afterAgentResponse` records the assistant fragment and submits the completed turn through the existing Journal.
- Missing fields, cancelled/aborted generations, duplicate callbacks, unsupported modes, timeouts, or Broker failure fail open without blocking Cursor.
- Capture is opt-in during install, bounded by byte limits, ignores attachments/file bodies, and supports a project-level disable marker.
- Cursor Tab hooks are not used; MLink memory applies to Cursor Agent Chat/Cmd+K/CLI only.

Hook configuration is backed up, ownership-tagged, drift-checked, and removable without changing unrelated Cursor hooks.

### MCP

Add local stdio server:

```text
mlink mcp serve
```

Use the official `github.com/modelcontextprotocol/go-sdk` pinned to v1.7.0. Tools:

```text
mlink_memory_search(query, max_items?)
mlink_memory_status()
```

Both are read-only. `memory_search` is the authoritative query-specific recall path; `sessionStart` is only best-effort startup context. `memory_search` routes through the fixed Owner identity and returns bounded structured context; `memory_status` returns non-secret health and layer availability. No delete/capture tool is exposed in the first Cursor release.

MLink semantically merges global `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "mlink-memory": {
      "type": "stdio",
      "command": "/absolute/path/to/mlink",
      "args": ["mcp", "serve"]
    }
  }
}
```

No API key is stored in Cursor config. The server reaches the private local Broker socket.

### AGENTS.md

Update the existing root `AGENTS.md`; do not create lowercase `agent.md`.

It must state for contributors to this repository:

- supported adapters are Codex, Pi, Hermes, and local Cursor;
- Cursor uses Hooks for capture/startup context and MCP for query-specific recall;
- Agents should call `mlink_memory_search` when prior decisions/preferences/history materially affect a task;
- never send raw secrets, full external IDs, or memory contents to logs/tests;
- distribution wrappers remain thin and canonical behavior stays in Go;
- maintenance, release, and branch safety rules from the parent instructions remain mandatory.

Root `AGENTS.md` does not configure Cursor in unrelated repositories. Global guidance is delivered by the user-level `sessionStart` hook and MCP tool descriptions.

## Installer and TUI

Add Cursor to detection, selection, preview, apply, Doctor, backup, restore, uninstall, and narrow TUI layout. Install verification requires hook config present plus a runtime heartbeat from one Cursor hook. MCP is verified separately through an initialize/list-tools/call stdio test and Cursor's configured-server inspection when the CLI is installed.

## Verification

- Cursor Hook fixture tests for every event, malformed JSON, missing IDs, timeouts, duplicate turns, and fail-open behavior;
- semantic merge/removal tests for existing Cursor hooks and MCP config;
- official MCP SDK initialize/list-tools/call contract tests;
- real local Cursor config preview before mutation and manual first-turn acceptance after confirmation;
- full Go tests under Go 1.27.0, shuffle, vet, and model/auth hash comparison.

## Official references

- Cursor Hooks: `https://prod.cursor.com/docs/hooks`
- Cursor MCP: `https://prod.cursor.com/docs/mcp`
- Cursor AGENTS.md: `https://prod.cursor.com/docs/rules`
- Cursor CLI MCP: `https://prod.cursor.com/docs/cli/mcp`
- Official MCP Go SDK: `https://github.com/modelcontextprotocol/go-sdk`
