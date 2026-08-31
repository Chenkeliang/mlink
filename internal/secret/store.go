package secret

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

var ErrUnsupported = errors.New("system keychain is unsupported")

var keychainEncodingPrefix = []byte("mlink:v1:")

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
	encoded := encodeKeychainSecret(secret)
	defer wipeBytes(encoded)
	args := []string{
		"/usr/bin/security",
		"add-generic-password",
		"-U",
		"-s", "dev.mlink",
		"-a", account,
		"-w",
	}
	if err := k.writer().Write(ctx, args, encoded); err != nil {
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
	defer wipeBytes(output)
	decoded, err := decodeKeychainSecret(bytes.TrimSuffix(output, []byte{'\n'}))
	if err != nil {
		return nil, fmt.Errorf("decode MLink secret for %q: %w", account, err)
	}
	return decoded, nil
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

func encodeKeychainSecret(value []byte) []byte {
	encoded := make([]byte, len(keychainEncodingPrefix)+base64.RawURLEncoding.EncodedLen(len(value)))
	copy(encoded, keychainEncodingPrefix)
	base64.RawURLEncoding.Encode(encoded[len(keychainEncodingPrefix):], value)
	return encoded
}

func decodeKeychainSecret(value []byte) ([]byte, error) {
	if !bytes.HasPrefix(value, keychainEncodingPrefix) {
		return append([]byte(nil), value...), nil
	}
	encoded := value[len(keychainEncodingPrefix):]
	decoded := make([]byte, base64.RawURLEncoding.DecodedLen(len(encoded)))
	count, err := base64.RawURLEncoding.Decode(decoded, encoded)
	if err != nil {
		wipeBytes(decoded)
		return nil, errors.New("invalid versioned Keychain value")
	}
	return decoded[:count], nil
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
