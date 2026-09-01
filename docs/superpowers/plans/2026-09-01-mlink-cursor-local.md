# MLink Local Cursor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox syntax.

**Goal:** Add local Cursor Desktop/CLI capture via official Hooks and query-specific recall via an official MCP stdio server.

**Architecture:** Cursor user hooks call the native binary and fail open. MCP is the authoritative read path through the private Broker socket. The adapter is fixed to the Owner policy and never changes Cursor model configuration.

**Tech Stack:** Go 1.27.0, Cursor Hooks v1, official MCP Go SDK v1.7.0.

**Spec:** `docs/superpowers/specs/2026-09-01-mlink-cursor-npm-release-design.md`

## Constraints

- Local Cursor only; no Cloud Agent or Tab memory.
- Capture is opt-in, bounded and project-disableable.
- sessionStart context is best-effort; MCP search is authoritative.
- Semantic merge/backup/uninstall preserve unrelated Cursor config.

### Task 1: Cursor hook adapter

**Files:** create `internal/adapter/cursor/{config.go,handler.go,*_test.go,testdata/*}`; modify app Agent enum/detection.

- [ ] Write official fixture tests for sessionStart, beforeSubmitPrompt, afterAgentResponse, duplicate/cancel/malformed/oversize/fail-open.
- [ ] Run tests; expect missing package.
- [ ] Implement bounded JSON handlers using existing Broker client and fragment pairing.
- [ ] Implement semantic user-hook merge/remove with absolute binary command and ownership markers.
- [ ] Run tests and commit `feat: capture cursor agent turns`.

### Task 2: Cursor routing and lifecycle

**Files:** modify config validation, runtime grants, app install/restore/uninstall/status, CLI/TUI/Doctor and tests.

- [ ] Write failing tests proving Cursor uses fixed Owner generated IDs and no model settings.
- [ ] Add Cursor to adapters, dynamic TUI selection, previews, backups, removal and heartbeat Doctor.
- [ ] Run affected/full tests and commit `feat: install cursor memory hooks`.

### Task 3: MCP server

**Files:** add official SDK v1.7.0; create `internal/mcpserver/server.go`, tests; modify CLI run/runtime.

- [ ] Write initialize/list-tools/call tests for `mlink_memory_search` and `mlink_memory_status`, bounds, cancellation and stdout purity.
- [ ] Implement `mlink mcp serve` over stdio using the official SDK and fixed Owner Broker route.
- [ ] Run MCP/full tests and commit `feat: expose mlink memory over mcp`.

### Task 4: Cursor MCP config and instructions

**Files:** create Cursor MCP semantic merge/remove package/tests; modify installer; update `AGENTS.md`.

- [ ] Write failing tests for existing `~/.cursor/mcp.json`, unrelated servers, absolute binary, uninstall, narrow TUI.
- [ ] Implement global `mlink-memory` stdio config with no secrets.
- [ ] Update AGENTS.md with Cursor/support/security/release constraints.
- [ ] Run tests and commit `feat: configure cursor mcp memory`.

### Task 5: Real Cursor acceptance

- [ ] Generate exact preview and prove Cursor/model files unchanged before Apply.
- [ ] Apply after exact Plan confirmation; verify Cursor reload, hook heartbeat, MCP list-tools/search, one capture, Hub visibility and uninstall preview.
- [ ] Run full/shuffle/vet and record evidence.
- [ ] Commit `test: accept local cursor memory`.

