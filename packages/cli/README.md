# @mlink/cli

Thin, checksum-verifying bootstrap for the native [MLink](https://github.com/Chenkeliang/mlink) executable.

```bash
npx @mlink/cli setup
```

Installing this npm package does not modify Agent configuration. Running it downloads the exact release for the local platform, verifies the release and binary SHA-256 values embedded in the package, installs `~/.local/bin/mlink` atomically, and starts the native guided TUI.
