# @mlink/cli

> **Status: implemented in the repository, not published to npm yet.**

This package is a thin, checksum-verifying bootstrap for the native [MLink](https://github.com/Chenkeliang/mlink) executable. Once a real package version is published, the intended entry point is:

```bash
npx @mlink/cli setup
```

Do not rely on that command while npm still reports `@mlink/cli` as missing.

The bootstrap does not implement Broker, Provider, Hook, identity, backup, or configuration behavior in JavaScript. It only:

1. selects the exact native asset declared in the package's `release.json`;
2. verifies the release archive and native binary SHA-256 values;
3. installs `~/.local/bin/mlink` atomically;
4. starts the native guided TUI.

Installing the npm package alone never modifies Agent configuration. Native MLink still owns preview, exact-Plan confirmation, backup, apply, verification, and uninstall.

For local package development:

```bash
npm test
node bin/mlink.mjs setup
```

The current bootstrap supports only assets explicitly present in `release.json`; the accepted release platform is macOS arm64.
