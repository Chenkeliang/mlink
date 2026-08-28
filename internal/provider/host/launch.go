package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"mlink/internal/connection"
	"mlink/internal/provider/manifest"
)

type Config struct {
	Snapshot          connection.ConnectionSnapshot
	Manifest          manifest.Manifest
	PackageDirectory  string
	RuntimeDirectory  string
	Secrets           map[string]string
	ParentEnvironment []string
}

func Start(ctx context.Context, config Config) (Session, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("resolve current MLink executable")
	}
	return startBundledSession(ctx, config, executable)
}

func startBundledSession(ctx context.Context, config Config, executable string) (*processSession, error) {
	realExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, errors.New("resolve bundled MLink executable")
	}
	info, err := os.Stat(realExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("bundled MLink executable is not executable")
	}
	packageDirectory, err := filepath.EvalSymlinks(config.PackageDirectory)
	if err != nil {
		return nil, errors.New("resolve Provider package directory")
	}
	info, err = os.Stat(packageDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("Provider package directory is invalid")
	}
	argv, err := manifest.ResolveBundled(config.Manifest, realExecutable)
	if err != nil {
		return nil, err
	}
	environment := config.ParentEnvironment
	if environment == nil {
		environment = os.Environ()
	}
	return startSession(ctx, startConfig{
		Snapshot:          config.Snapshot,
		Manifest:          config.Manifest,
		Argv:              argv,
		WorkingDirectory:  packageDirectory,
		RuntimeDirectory:  config.RuntimeDirectory,
		Secrets:           cloneSecrets(config.Secrets),
		ParentEnvironment: append([]string(nil), environment...),
	})
}
