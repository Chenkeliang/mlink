package workspacebackup

import (
	"fmt"
	"time"

	"mlink/internal/version"
)

const FormatV1 = "mlink-full-backup/v1"

type Header struct {
	Format string `json:"format"`
}

type Section string

const (
	SectionManifestData Section = "manifest"
	SectionChecksums    Section = "checksums"
	SectionCore         Section = "core"
	SectionKnowledge    Section = "knowledge"
	SectionMLink        Section = "mlink"
	SectionIdentity     Section = "identity"
	SectionSecrets      Section = "secrets"
	SectionAgents       Section = "agents"
)

func (section Section) Valid() bool {
	switch section {
	case SectionManifestData, SectionChecksums, SectionCore, SectionKnowledge, SectionMLink, SectionIdentity, SectionSecrets, SectionAgents:
		return true
	default:
		return false
	}
}

type VolumeKind string

const (
	VolumeCore      VolumeKind = "core"
	VolumeKnowledge VolumeKind = "knowledge"
)

func (kind VolumeKind) Valid() bool { return kind == VolumeCore || kind == VolumeKnowledge }

type VolumeManifest struct {
	Kind         VolumeKind `json:"kind"`
	Name         string     `json:"name"`
	LogicalBytes int64      `json:"logical_bytes"`
	FileCount    int        `json:"file_count"`
	SHA256       string     `json:"sha256"`
}

type ProviderManifest struct {
	ProviderID      string           `json:"provider_id"`
	DriverVersion   string           `json:"driver_version"`
	InstanceID      string           `json:"instance_id"`
	CoreImageDigest string           `json:"core_image_digest"`
	HubImageDigest  string           `json:"hub_image_digest"`
	Volumes         []VolumeManifest `json:"volumes"`
}

type ControlPlaneManifest struct {
	InstallationID    string `json:"installation_id"`
	InstanceID        string `json:"instance_id"`
	OwnerUserID       string `json:"owner_user_id"`
	OwnerTeamID       string `json:"owner_team_id"`
	OwnerAgentID      string `json:"owner_agent_id"`
	OwnerAssetID      string `json:"owner_asset_id"`
	DynamicAgentLimit int    `json:"dynamic_agent_limit"`
}

type PrincipalAgentManifest struct {
	Fingerprint    string `json:"fingerprint"`
	RouteKind      string `json:"route_kind"`
	BackendUserID  string `json:"backend_user_id"`
	BackendTeamID  string `json:"backend_team_id"`
	BackendAgentID string `json:"backend_agent_id"`
	BackendAssetID string `json:"backend_asset_id"`
	State          string `json:"state"`
}

type SectionManifest struct {
	Name         Section `json:"name"`
	CipherBytes  int64   `json:"cipher_bytes"`
	CipherSHA256 string  `json:"cipher_sha256"`
}

type Manifest struct {
	Format          string                   `json:"format"`
	CreatedAt       time.Time                `json:"created_at"`
	MLink           version.Info             `json:"mlink"`
	Provider        ProviderManifest         `json:"provider"`
	ControlPlane    ControlPlaneManifest     `json:"control_plane"`
	PrincipalAgents []PrincipalAgentManifest `json:"principal_agents"`
	Agents          []string                 `json:"agents"`
	Sections        []SectionManifest        `json:"sections"`
}

func (manifest Manifest) String() string {
	return fmt.Sprintf("Manifest{Format:%q MLink:%q Provider:%q Volumes:%d Agents:%d PrincipalAgents:%d Instance:<redacted> ControlPlane:<redacted>}",
		manifest.Format, manifest.MLink.Version, manifest.Provider.ProviderID, len(manifest.Provider.Volumes), len(manifest.Agents), len(manifest.PrincipalAgents))
}

func (manifest Manifest) GoString() string { return manifest.String() }
