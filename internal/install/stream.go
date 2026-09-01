package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
)

var ErrStreamLimit = errors.New("stream exceeds configured limit")

type StreamRunner interface {
	RunStream(context.Context, []string, io.Reader, io.Writer) error
}

type LocalStreamRunner struct{}

type EnvironmentRunner interface {
	RunEnvironment(context.Context, []string, map[string][]byte, io.Reader) ([]byte, error)
}

type LocalEnvironmentRunner struct{}

var environmentNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func (LocalEnvironmentRunner) RunEnvironment(ctx context.Context, argv []string, environment map[string][]byte, stdin io.Reader) ([]byte, error) {
	if len(argv) == 0 || argv[0] == "" || len(environment) == 0 {
		return nil, errors.New("environment command and protected values are required")
	}
	values := append([]string(nil), os.Environ()...)
	for name, value := range environment {
		if !environmentNamePattern.MatchString(name) || len(value) == 0 || bytes.IndexByte(value, 0) >= 0 {
			return nil, errors.New("protected environment value is invalid")
		}
		values = append(values, name+"="+string(value))
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = values
	command.Stdin = stdin
	var output bytes.Buffer
	command.Stdout = &LimitedWriter{Writer: &output, Limit: 64 << 10}
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("environment command failed with exit code %d", exitErr.ExitCode())
		}
		return nil, errors.New("environment command failed")
	}
	return output.Bytes(), nil
}

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
