package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

var ErrUnsupported = errors.New("system keychain is unsupported")

type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Runner interface {
	Run(context.Context, []string, io.Reader) ([]byte, error)
}

type Writer interface {
	Write(context.Context, []string, []byte) error
}

type Keychain struct {
	Runner Runner
	Writer Writer
}

func (k Keychain) Put(ctx context.Context, account string, secret []byte) error {
	if account == "" {
		return errors.New("keychain account is required")
	}
	if len(secret) == 0 {
		return errors.New("keychain secret is required")
	}
	if bytes.ContainsAny(secret, "\r\n") {
		return errors.New("keychain secret must be a single line")
	}
	args := []string{
		"/usr/bin/security",
		"add-generic-password",
		"-U",
		"-s", "dev.mlink",
		"-a", account,
		"-w",
	}
	if err := k.writer().Write(ctx, args, secret); err != nil {
		return fmt.Errorf("store MLink secret for %q: %w", account, err)
	}
	return nil
}

func (k Keychain) Get(ctx context.Context, account string) ([]byte, error) {
	if account == "" {
		return nil, errors.New("keychain account is required")
	}
	args := []string{
		"/usr/bin/security",
		"find-generic-password",
		"-s", "dev.mlink",
		"-a", account,
		"-w",
	}
	output, err := k.runner().Run(ctx, args, nil)
	if err != nil {
		if keychainItemNotFound(err) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("read MLink secret for %q: %w", account, err)
	}
	return bytes.TrimSuffix(output, []byte{'\n'}), nil
}

func (k Keychain) Delete(ctx context.Context, account string) error {
	if account == "" {
		return errors.New("keychain account is required")
	}
	args := []string{
		"/usr/bin/security",
		"delete-generic-password",
		"-s", "dev.mlink",
		"-a", account,
	}
	if _, err := k.runner().Run(ctx, args, nil); err != nil {
		if keychainItemNotFound(err) {
			return fs.ErrNotExist
		}
		return fmt.Errorf("delete MLink secret for %q: %w", account, err)
	}
	return nil
}

func keychainItemNotFound(err error) bool {
	var exitError interface{ ExitCode() int }
	return errors.As(err, &exitError) && exitError.ExitCode() == 44
}

func (k Keychain) runner() Runner {
	if k.Runner != nil {
		return k.Runner
	}
	return defaultRunner()
}

func (k Keychain) writer() Writer {
	if k.Writer != nil {
		return k.Writer
	}
	return defaultWriter()
}
