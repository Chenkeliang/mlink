# MLink Cursor and npm Release Design

Date: 2026-09-01

Status: approved for implementation

## Purpose

Add local Cursor Desktop/CLI memory without changing Cursor's model or subscription, prepare the canonical native binary for npm/npx bootstrap distribution, update project Agent instructions, and merge the verified release branch into `main` locally.

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

- `sessionStart` performs bounded generic Owner recall and returns `additional_context` capped by characters/items/time.
- `beforeSubmitPrompt` records the user fragment keyed by conversation/generation.
- `afterAgentResponse` records the assistant fragment and submits the completed turn through the existing Journal.
- Missing fields, unsupported modes, timeouts, or Broker failure fail open without blocking Cursor.
- Cursor Tab hooks are not used; MLink memory applies to Cursor Agent Chat/Cmd+K/CLI only.

Hook configuration is backed up, ownership-tagged, drift-checked, and removable without changing unrelated Cursor hooks.

### MCP

Add local stdio server:

```text
mlink mcp serve
```

Use the official `github.com/modelcontextprotocol/go-sdk` pinned to a reviewed stable version. Tools:

```text
mlink_memory_search(query, max_items?)
mlink_memory_status()
```

Both are read-only. `memory_search` routes through the fixed Owner identity and returns bounded structured context; `memory_status` returns non-secret health and layer availability. No delete/capture tool is exposed in the first Cursor release.

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

It must state:

- supported adapters are Codex, Pi, Hermes, and local Cursor;
- Cursor uses Hooks for capture/startup context and MCP for query-specific recall;
- Agents should call `mlink_memory_search` when prior decisions/preferences/history materially affect a task;
- never send raw secrets, full external IDs, or memory contents to logs/tests;
- distribution wrappers remain thin and canonical behavior stays in Go;
- maintenance, release, and branch safety rules from the parent instructions remain mandatory.

## Installer and TUI

Add Cursor to detection, selection, preview, apply, Doctor, backup, restore, uninstall, and narrow TUI layout. Install verification requires hook config present plus a runtime heartbeat from one Cursor hook. MCP is verified separately through an initialize/list-tools/call stdio test and Cursor's configured-server inspection when the CLI is installed.

## npm/npx distribution

Package name follows the existing contract: `@mlink/cli`.

The npm package contains only:

- `package.json` with `bin` entries;
- a small Node bootstrap script;
- release public key/checksum verifier;
- platform mapping for supported signed native artifacts.

It may:

1. resolve OS/architecture;
2. select an explicitly requested package version;
3. download the same-version native artifact from the canonical release manifest;
4. verify SHA-256 and signature;
5. install in a user-writable cache/bin directory;
6. execute the native binary.

It must not contain installation business logic, fetch unpinned `latest`, modify Agent config during npm install, or require Node after the native binary is installed. `npx @mlink/cli setup` delegates to `mlink` TUI/setup.

Publishing credentials and the actual npm publish action remain user-owned and out of scope. The repository produces a packable/testable tarball.

## Native release metadata

Add version variables populated by `-ldflags`, `mlink version --json`, deterministic release manifest schema, checksums, and signature-verification hooks. The initial implementation may use a test signing key only in fixtures; no private release key enters the repository.

## Local main integration

After all tests pass:

- verify every commit in `main..feat_product_design` belongs to MLink work;
- preserve the user's unstaged `README.md` by committing its intended final content separately or asking before merge if it remains dirty;
- merge `feat_product_design` into local `main` without push;
- run the full suite on `main`;
- keep external Hub/MemoryCore state running.

## Verification

- Cursor Hook fixture tests for every event, malformed JSON, missing IDs, timeouts, duplicate turns, and fail-open behavior;
- semantic merge/removal tests for existing Cursor hooks and MCP config;
- official MCP SDK initialize/list-tools/call contract tests;
- real local Cursor config preview before mutation and manual first-turn acceptance after confirmation;
- npm `pack`, install-from-tarball, fake signed-manifest download, checksum/signature rejection, offline-cache, and no-postinstall-mutation tests;
- full Go tests, npm tests, shuffle, fixed toolchain, vet, and model/auth hash comparison;
- local merge to `main` only after all gates pass.

## Official references

- Cursor Hooks: `https://prod.cursor.com/docs/hooks`
- Cursor MCP: `https://prod.cursor.com/docs/mcp`
- Cursor AGENTS.md: `https://prod.cursor.com/docs/rules`
- Cursor CLI MCP: `https://prod.cursor.com/docs/cli/mcp`
- Official MCP Go SDK: `https://github.com/modelcontextprotocol/go-sdk`
