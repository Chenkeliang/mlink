package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

type RoutingTarget struct {
	local      install.Target
	hermes     install.Target
	hermesHome string
}

func NewRoutingTarget(local, hermesTarget install.Target, hermesHome string) (*RoutingTarget, error) {
	if local == nil || hermesTarget == nil {
		return nil, errors.New("local and Hermes targets are required")
	}
	home := filepath.Clean(hermesHome)
	if !filepath.IsAbs(home) || home == string(filepath.Separator) {
		return nil, errors.New("Hermes home must be an absolute non-root path")
	}
	return &RoutingTarget{local: local, hermes: hermesTarget, hermesHome: home}, nil
}

func (target *RoutingTarget) Read(ctx context.Context, path string) ([]byte, fs.FileMode, error) {
	return target.forPath(path).Read(ctx, path)
}

func (target *RoutingTarget) WriteAtomic(ctx context.Context, path string, content []byte, mode fs.FileMode) error {
	return target.forPath(path).WriteAtomic(ctx, path, content, mode)
}

func (target *RoutingTarget) Remove(ctx context.Context, path string) error {
	return target.forPath(path).Remove(ctx, path)
}

func (target *RoutingTarget) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	if len(args) != 0 && args[0] == "hermes" {
		return target.hermes.Run(ctx, args, stdin)
	}
	return target.local.Run(ctx, args, stdin)
}

func (target *RoutingTarget) forPath(path string) install.Target {
	clean := filepath.Clean(path)
	if clean != target.hermesHome && strings.HasPrefix(clean, target.hermesHome+string(filepath.Separator)) {
		return target.hermes
	}
	return target.local
}
