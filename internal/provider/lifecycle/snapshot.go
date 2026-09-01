package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"mlink/internal/install"
	"mlink/internal/workspacebackup"
)

var ErrSnapshotUnavailable = errors.New("Provider snapshot lifecycle is unavailable")

type SnapshotVolume struct {
	Kind         workspacebackup.VolumeKind
	Name         string
	LogicalBytes int64
	FileCount    int
}

type SnapshotSource struct {
	ProviderID    string
	DriverVersion string
	InstanceID    string
	Owned         bool
	CoreContainer string
	HubContainer  string
	CoreImage     string
	HubImage      string
	Volumes       []SnapshotVolume
}

func (source SnapshotSource) Clone() SnapshotSource {
	clone := source
	clone.Volumes = append([]SnapshotVolume(nil), source.Volumes...)
	return clone
}

func (source SnapshotSource) String() string {
	return fmt.Sprintf("SnapshotSource{ProviderID:%q DriverVersion:%q Owned:%t Volumes:%d Instance:<redacted>}", source.ProviderID, source.DriverVersion, source.Owned, len(source.Volumes))
}

func (source SnapshotSource) GoString() string { return source.String() }

type BackupRequest struct {
	ProviderID            string
	ApproveExternalSource bool
}

func (request BackupRequest) String() string {
	return fmt.Sprintf("BackupRequest{ProviderID:%q ApproveExternalSource:%t}", request.ProviderID, request.ApproveExternalSource)
}

func (request BackupRequest) GoString() string { return request.String() }

type RestoreRequest struct {
	ProviderID      string
	BundlePath      string
	CoreContainer   string
	CoreVolume      string
	KnowledgeVolume string
	GatewayToken    []byte
	LLMAPIKey       []byte
	OwnerUserKey    []byte
}

func (request RestoreRequest) String() string {
	return fmt.Sprintf("RestoreRequest{ProviderID:%q Bundle:%q CoreContainer:%q CoreVolume:%q KnowledgeVolume:%q GatewayToken:<redacted> LLMAPIKey:<redacted> OwnerUserKey:<redacted>}",
		request.ProviderID, filepath.Base(request.BundlePath), request.CoreContainer, request.CoreVolume, request.KnowledgeVolume)
}

func (request RestoreRequest) GoString() string { return request.String() }

func (request *RestoreRequest) Wipe() {
	if request == nil {
		return
	}
	for _, value := range [][]byte{request.GatewayToken, request.LLMAPIKey, request.OwnerUserKey} {
		for index := range value {
			value[index] = 0
		}
	}
	request.GatewayToken = nil
	request.LLMAPIKey = nil
	request.OwnerUserKey = nil
}

type SnapshotDriver interface {
	Detect(context.Context) (SnapshotSource, error)
	PlanBackup(context.Context, BackupRequest) (install.ChangeSet, error)
	StreamSection(context.Context, BackupRequest, workspacebackup.Section, io.Writer) (workspacebackup.VolumeManifest, error)
	PlanRestore(context.Context, RestoreRequest, workspacebackup.ProviderManifest) (install.ChangeSet, error)
	ApplySection(context.Context, RestoreRequest, workspacebackup.Section, io.Reader) error
	VerifyRestore(context.Context, RestoreRequest, workspacebackup.Manifest) error
}

type SnapshotRegistration struct {
	ProviderID string
	Driver     SnapshotDriver
}

type SnapshotRegistry struct{ values map[string]SnapshotDriver }

func NewSnapshotRegistry(registrations ...SnapshotRegistration) (*SnapshotRegistry, error) {
	registry := &SnapshotRegistry{values: make(map[string]SnapshotDriver, len(registrations))}
	for _, registration := range registrations {
		if !providerIDPattern.MatchString(registration.ProviderID) || registration.Driver == nil {
			return nil, errors.New("valid Provider snapshot registration is required")
		}
		if _, exists := registry.values[registration.ProviderID]; exists {
			return nil, fmt.Errorf("duplicate Provider snapshot lifecycle %q", registration.ProviderID)
		}
		registry.values[registration.ProviderID] = registration.Driver
	}
	return registry, nil
}

func (registry *SnapshotRegistry) Get(providerID string) (SnapshotDriver, error) {
	if registry == nil {
		return nil, ErrSnapshotUnavailable
	}
	value, exists := registry.values[providerID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotUnavailable, providerID)
	}
	return value, nil
}
