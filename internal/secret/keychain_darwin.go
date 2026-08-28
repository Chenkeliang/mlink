//go:build darwin

package secret

import (
	"context"
	"io"
	"os/exec"
)

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Stdin = stdin
	return command.Output()
}

func defaultRunner() Runner {
	return commandRunner{}
}
