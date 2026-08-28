package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
)

type Dependencies struct {
	Stdout         io.Writer
	Stderr         io.Writer
	ServeTencentDB func(context.Context) error
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	if slices.Equal(args, []string{"provider", "run", "tencentdb"}) {
		if deps.ServeTencentDB == nil || deps.ServeTencentDB(ctx) != nil {
			writeLine(deps.Stderr, "mlink provider command failed")
			return 1
		}
		return 0
	}
	if isPlannedCommand(args) {
		writeLine(deps.Stderr, "MLink command not implemented")
		return 2
	}
	writeLine(deps.Stderr, "unsupported MLink command")
	return 2
}

func isPlannedCommand(args []string) bool {
	for _, command := range [][]string{
		{"install"},
		{"status"},
		{"doctor"},
		{"backup"},
		{"uninstall"},
		{"broker", "serve"},
		{"hook", "codex"},
	} {
		if slices.Equal(args, command) {
			return true
		}
	}
	return false
}

func writeLine(w io.Writer, message string) {
	if w != nil {
		_, _ = fmt.Fprintln(w, message)
	}
}
