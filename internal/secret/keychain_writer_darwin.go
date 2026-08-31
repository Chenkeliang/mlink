//go:build darwin

package secret

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

const (
	keychainPromptLimit  = 4 * 1024
	keychainWriteTimeout = 15 * time.Second
)

type promptedWriter struct {
	timeout time.Duration
}

func (writer promptedWriter) Write(ctx context.Context, args []string, secret []byte) error {
	if len(args) == 0 || len(secret) == 0 {
		return errors.New("keychain command and secret are required")
	}
	timeout := writer.timeout
	if timeout <= 0 {
		timeout = keychainWriteTimeout
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wrapperArgs := []string{"-c", `trap '' HUP; exec "$@"`, "mlink-keychain"}
	wrapperArgs = append(wrapperArgs, args...)
	command := exec.CommandContext(commandContext, "/bin/sh", wrapperArgs...)
	command.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	terminal, err := pty.Start(command)
	if err != nil {
		return errors.New("start protected keychain prompt")
	}
	defer terminal.Close()

	reader := bufio.NewReader(terminal)
	input := append(append([]byte(nil), secret...), '\n')
	defer wipeSecret(input)
	for prompt := 0; prompt < 2; prompt++ {
		if err := waitForPromptContext(commandContext, reader); err != nil {
			_ = terminal.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
			if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
				return errors.New("keychain prompt timed out")
			}
			return errors.New("keychain password prompt failed")
		}
		if _, err := terminal.Write(input); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return errors.New("answer keychain password prompt")
		}
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	grace := time.NewTimer(250 * time.Millisecond)
	defer grace.Stop()
	var commandErr error
	select {
	case commandErr = <-done:
	case <-grace.C:
		_ = terminal.Close()
		select {
		case commandErr = <-done:
		case <-commandContext.Done():
			commandErr = commandContext.Err()
		}
	case <-commandContext.Done():
		_ = terminal.Close()
		commandErr = commandContext.Err()
	}
	if commandErr != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return errors.New("keychain command timed out")
		}
		return fmt.Errorf("keychain command failed: %w", commandErr)
	}
	return nil
}

func waitForPromptContext(ctx context.Context, reader *bufio.Reader) error {
	result := make(chan error, 1)
	go func() { result <- waitForPrompt(reader) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForPrompt(reader *bufio.Reader) error {
	previous := byte(0)
	for count := 0; count < keychainPromptLimit; count++ {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if previous == ':' && value == ' ' {
			return nil
		}
		previous = value
	}
	return errors.New("keychain prompt exceeds size limit")
}

func wipeSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func defaultWriter() Writer {
	return promptedWriter{}
}
