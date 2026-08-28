package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

type CommandRunner interface {
	Run(context.Context, []string, io.Reader) ([]byte, error)
}

type LocalTarget struct {
	Runner CommandRunner
}

func (LocalTarget) Read(_ context.Context, target string) ([]byte, fs.FileMode, error) {
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode().Perm(), nil
}

func (LocalTarget) WriteAtomic(_ context.Context, target string, content []byte, mode fs.FileMode) error {
	if target == "" {
		return errors.New("target path is required")
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".mlink-*")
	if err != nil {
		return fmt.Errorf("create temporary target: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set temporary target mode: %w", err)
	}
	if _, err := temp.Write(content); err != nil {
		return fmt.Errorf("write temporary target: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary target: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary target: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		return fmt.Errorf("replace target: %w", err)
	}
	committed = true
	if err := os.Chmod(target, mode.Perm()); err != nil {
		return fmt.Errorf("set target mode: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open target directory: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync target directory: %w", err)
	}
	return nil
}

func (LocalTarget) Remove(_ context.Context, target string) error {
	err := os.Remove(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (t LocalTarget) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("command is required")
	}
	if t.Runner != nil {
		return t.Runner.Run(ctx, args, stdin)
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Stdin = stdin
	return command.Output()
}
