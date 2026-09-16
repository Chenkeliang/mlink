# MLink Project Constraints

These constraints apply to all work inside this repository. They supplement the parent workspace instructions.

## Product Boundary

- MLink is a memory connection layer for Agents. It must not proxy Agent LLM traffic or change model providers, base URLs, API keys, subscription sessions, or authentication flows.
- Agent adapters depend only on MLink's canonical memory model. Provider-specific types and behavior stay behind the Provider boundary.
- The first release supports Codex, Pi, Hermes Agent, local Cursor Desktop/CLI, and TencentDB MemoryCore. Cursor Cloud Agent and Tab memory are outside the local adapter boundary.
- Cursor lifecycle capture uses user-level Hooks and must fail open. Query-specific recall uses the local `mlink-memory` stdio MCP server and is authoritative; `sessionStart` context is best-effort only.
- Cursor integration must never read or modify model selection, provider, subscription, API-key, authentication, or general editor settings. Owned changes are limited to semantic entries in `~/.cursor/hooks.json` and `~/.cursor/mcp.json`.

## Implementation Stack

- MLink Core, CLI, TUI, Broker, and Provider Host are implemented in Go and released as one native `mlink` executable.
- Do not migrate the core to Node.js, Bun, TypeScript, or Python without an approved architecture change.
- The native executable must not require Node.js, Bun, Python, npm, or another language runtime to start or operate its bundled functionality.
- Provider implementations are language-neutral. A Provider may use Go, TypeScript, Python, or another language when it communicates through the versioned process protocol and owns its runtime dependencies.
- Do not duplicate core behavior in distribution wrappers or Provider implementations.

## Distribution Contract

- Signed platform binaries and their release metadata are the canonical artifacts.
- Homebrew is the primary macOS package-manager entry. The npm package is a thin bootstrap entry for `npx @mlink/cli setup` or global installation; future WinGet, Scoop, or PyPI packages follow the same wrapper model.
- A distribution wrapper may only resolve OS/architecture, select an exact matching MLink version, download it, verify its checksum/signature, install it, and execute it.
- A wrapper must never fetch an unpinned `latest` binary at runtime, contain Broker or configuration business logic, or silently change Agent configuration during package installation.
- Agent configuration changes happen only after the user explicitly runs `mlink setup` and accepts the normal preview. They remain backed up, verifiable, and removable by MLink.
- Keep direct binary installation fully supported; npm, Bun, and Python are optional installation conveniences, not runtime requirements.
- Release builds use Go 1.27.x and inject version, commit, build date, and supported config-schema range. Never publish a wrapper before the referenced GitHub Release asset and checksum exist.

## Provider Packaging

- Provider processes are launched without a shell and communicate over the documented versioned protocol.
- Providers must ship a deterministic entrypoint and declare their runtime requirements. The Broker must not run `npm install`, `pip install`, or other dependency installation while starting a Provider.
- Adding a new Provider must not require changes to Codex, Pi, or Hermes adapters.

## Change Discipline

- Treat changes to the core language, wire protocol, canonical model, identity keys, protected Agent configuration, or distribution trust model as architecture changes requiring a reviewed design update first.
- Keep packaging code thin and test it against the same released binary used by direct installation.
- Do not add a new package channel or runtime merely for symmetry; add it only for a demonstrated user installation path.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **mlink** (9194 symbols, 30182 relationships, 300 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> If any GitNexus tool warns the index is stale, run `npx gitnexus analyze` in terminal first.

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `gitnexus_impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `gitnexus_detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `gitnexus_query({query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `gitnexus_context({name: "symbolName"})`.

## Never Do

- NEVER edit a function, class, or method without first running `gitnexus_impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `gitnexus_rename` which understands the call graph.
- NEVER commit changes without running `gitnexus_detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/mlink/context` | Codebase overview, check index freshness |
| `gitnexus://repo/mlink/clusters` | All functional areas |
| `gitnexus://repo/mlink/processes` | All execution flows |
| `gitnexus://repo/mlink/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
