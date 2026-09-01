package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

var ErrStreamLimit = errors.New("stream exceeds configured limit")

type StreamRunner interface {
	RunStream(context.Context, []string, io.Reader, io.Writer) error
}

type LocalStreamRunner struct{}

func (LocalStreamRunner) RunStream(ctx context.Context, argv []string, stdin io.Reader, stdout io.Writer) error {
	if len(argv) == 0 || argv[0] == "" || stdout == nil {
		return errors.New("stream command and output are required")
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = stdin
	command.Stdout = stdout
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("stream command failed with exit code %d", exitErr.ExitCode())
		}
		return errors.New("stream command failed")
	}
	return nil
}

type LimitedWriter struct {
	Writer  io.Writer
	Limit   int64
	Written int64
}

func (writer *LimitedWriter) Write(value []byte) (int, error) {
	if writer == nil || writer.Writer == nil || writer.Limit < 0 {
		return 0, errors.New("valid limited writer is required")
	}
	if int64(len(value)) > writer.Limit-writer.Written {
		return 0, ErrStreamLimit
	}
	written, err := writer.Writer.Write(value)
	writer.Written += int64(written)
	return written, err
}
