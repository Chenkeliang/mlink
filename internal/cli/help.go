package cli

import (
	"fmt"
	"io"
	"strings"
)

func helpTopic(args []string) (string, bool) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return "", true
	}
	if len(args) == 2 && args[0] == "help" {
		return args[1], true
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		return args[0], true
	}
	return "", false
}

func renderHelp(writer io.Writer, topic string) int {
	if writer == nil {
		return 1
	}
	sections := map[string]string{
		"": `Usage: mlink [command] [options]

Memory-only connection layer for Codex, Cursor, Pi and Hermes.

Commands:
  install              Preview or apply the guided Agent installation
  adapter              Enable an Agent adapter on an existing installation
  status               Show installed adapters and active connection
  doctor               Run read-only dependency and runtime checks
  config diff          Compare current files with MLink ownership
  backup               List or restore automatic backups
  identity             List, bind, export or import identity state
  panel                Provision, inspect or open Memory Hub
  maintenance          Journal, credential and binary maintenance
  version              Print build and schema compatibility metadata

Run "mlink help <command>" for command-specific usage.
`,
		"install": `Usage: mlink install [codex] [pi] [hermes] [cursor] [options]

Options:
  --dry-run             Render an exact zero-write Plan
  --json                Render structured output
  --token-stdin         Read the MemoryCore token from stdin
  --install-secrets-stdin
                        Read all protected install inputs from stdin
  --apply-plan <id>     Apply only the freshly reproduced Plan
  --yes                 Required with --apply-plan
`,
		"maintenance": `Usage: mlink maintenance <area> <action> [options]

Areas:
  journal list|inspect|discard|acknowledge
  credentials rotate hermes-grant
  upgrade --candidate <absolute-path>

All maintenance mutations require an exact Plan ID and --yes.
`,
		"doctor": `Usage: mlink doctor [codex|pi|hermes|cursor] [--json]

Doctor is read-only. It checks identity, runtime, dependencies, Provider,
Agent adapters, Memory Hub and Journal state.
`,
	}
	text, exists := sections[strings.TrimSpace(topic)]
	if !exists {
		_, _ = fmt.Fprintf(writer, "Usage: mlink help [install|maintenance|doctor]\n")
		return 2
	}
	if topic != "" {
		text += "\nRelated: mlink doctor [agent] [--json]\n"
	}
	_, err := io.WriteString(writer, text)
	if err != nil {
		return 1
	}
	return 0
}
