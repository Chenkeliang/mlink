//go:build !darwin

package secret

import (
	"context"
	"io"
)

type unsupportedRunner struct{}

func (unsupportedRunner) Run(context.Context, []string, io.Reader) ([]byte, error) {
	return nil, ErrUnsupported
}

func defaultRunner() Runner {
	return unsupportedRunner{}
}
