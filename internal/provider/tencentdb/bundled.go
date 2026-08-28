package tencentdb

import (
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

func BundledManifest() manifest.Manifest {
	return manifest.Manifest{
		APIVersion:       "mlink.provider/v1",
		ProviderID:       providerID,
		Name:             "tencentdb",
		DisplayName:      "TencentDB MemoryCore",
		Version:          providerVersion,
		Entrypoint:       []string{"mlink", "provider", "run", "tencentdb"},
		Protocol:         "stdio-jsonrpc",
		ProtocolVersions: []string{protocol.Version},
		BackendCompat: manifest.BackendCompatibility{
			Product:     "tencentdb-memorycore",
			APIVersions: []string{"v3"},
		},
		DeclaredCapabilities: tencentDBCapabilities(),
		ExecutableSHA256:     "bundled",
	}
}
