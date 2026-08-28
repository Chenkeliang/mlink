# MLink Project Constraints

These constraints apply to all work inside this repository. They supplement the parent workspace instructions.

## Product Boundary

- MLink is a memory connection layer for Agents. It must not proxy Agent LLM traffic or change model providers, base URLs, API keys, subscription sessions, or authentication flows.
- Agent adapters depend only on MLink's canonical memory model. Provider-specific types and behavior stay behind the Provider boundary.
- The MVP supports Codex, Pi, Hermes Agent, and TencentDB MemoryCore only unless an approved design explicitly expands that scope.

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

## Provider Packaging

- Provider processes are launched without a shell and communicate over the documented versioned protocol.
- Providers must ship a deterministic entrypoint and declare their runtime requirements. The Broker must not run `npm install`, `pip install`, or other dependency installation while starting a Provider.
- Adding a new Provider must not require changes to Codex, Pi, or Hermes adapters.

## Change Discipline

- Treat changes to the core language, wire protocol, canonical model, identity keys, protected Agent configuration, or distribution trust model as architecture changes requiring a reviewed design update first.
- Keep packaging code thin and test it against the same released binary used by direct installation.
- Do not add a new package channel or runtime merely for symmetry; add it only for a demonstrated user installation path.
