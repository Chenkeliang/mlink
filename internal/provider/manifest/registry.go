package manifest

import (
	"errors"
	"slices"
)

var tencentDBBundledEntrypoint = []string{"mlink", "provider", "run", "tencentdb"}

func ResolveBundled(provider Manifest, currentExecutable string) ([]string, error) {
	if currentExecutable == "" {
		return nil, errors.New("current MLink executable path is empty")
	}
	if provider.ExecutableSHA256 != "bundled" ||
		provider.ProviderID != "dev.mlink.tencentdb" ||
		provider.Version != "0.1.0" ||
		!slices.Equal(provider.Entrypoint, tencentDBBundledEntrypoint) {
		return nil, errors.New("provider is not registered as a bundled MLink command")
	}
	return []string{currentExecutable, "provider", "run", "tencentdb"}, nil
}
