package app

import (
	"context"
	"fmt"
	"io"
	"io/fs"

	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/panel"
	"mlink/internal/secret"
	"mlink/internal/version"
)

type Agent string

const (
	Codex  Agent = "codex"
	Pi     Agent = "pi"
	Hermes Agent = "hermes"
	Cursor Agent = "cursor"
)

const (
	MemoryCoreTokenSecret = "memorycore_token"
	OwnerBindingSecret    = "owner_feishu_binding"
)

type InstallRequest struct {
	Agents            []Agent
	Connection        config.Connection
	OwnerSlug         string
	OwnerBindingSlot  config.BindingRef
	SecretInputs      map[string][]byte
	HermesMachine     string
	HermesHome        string
	DynamicAgentLimit int
}

type ControlPlaneBootstrapRequest struct {
	Connection        config.Connection
	OwnerSlug         string
	GatewayToken      []byte
	DynamicAgentLimit int
}

func (request ControlPlaneBootstrapRequest) String() string {
	return fmt.Sprintf("ControlPlaneBootstrapRequest{ConnectionID:%q ProviderID:%q OwnerSlug:%q GatewayToken:<redacted> DynamicAgentLimit:%d}",
		request.Connection.ID, request.Connection.ProviderID, request.OwnerSlug, request.DynamicAgentLimit)
}

func (request ControlPlaneBootstrapRequest) GoString() string { return request.String() }

func (request *ControlPlaneBootstrapRequest) Wipe() {
	for index := range request.GatewayToken {
		request.GatewayToken[index] = 0
	}
	request.GatewayToken = nil
}

type UninstallRequest struct {
	Agents      []Agent
	RemoveState bool
	BackupID    string
}

type RestoreRequest struct {
	BackupID string
}

type BlockingEventStore interface {
	ListStateDeletionBlockers(context.Context) ([]journal.Event, error)
}

type InstallPlanRecorder interface {
	RecordInstallPlan(context.Context, string, []string) error
}

type ActiveInstallStore interface {
	ActiveInstallPlan(context.Context) (string, error)
	MarkInstallRemoved(context.Context) error
}

type ControlPlaneStateStore interface {
	LoadControlPlane(context.Context) (journal.ControlPlaneState, error)
	MarkControlPlaneState(context.Context, string) error
}

type ControlPlaneProvisioner interface {
	PlanProvision(context.Context, controlplane.ProvisionRequest) (install.ChangeSet, error)
	ApplyProvision(context.Context, string, controlplane.ProvisionRequest) (controlplane.ProvisionResult, error)
}

type ProviderBackendUninstaller interface {
	PlanUninstall(context.Context) (install.ChangeSet, error)
}

type PrincipalAgentStateStore interface {
	GetPrincipalAgent(context.Context, string) (journal.PrincipalAgent, error)
	PutPrincipalAgent(context.Context, journal.PrincipalAgent) error
	ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error)
}

type JournalMaintenanceStore interface {
	ListUnresolvedEvents(context.Context) ([]journal.Event, error)
	ResolveUnresolvedEvent(context.Context, string, journal.ResolutionRequest) (journal.Event, error)
}

type MaintenanceRestarter interface {
	RestartBroker(context.Context) error
	RestartHermes(context.Context) error
}

type HermesGrantVerifier interface {
	VerifyHermesGrant(context.Context, string, []byte, []byte) error
}

type UpgradeCandidate struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
	Info    version.Info
}

type UpgradeCandidateLoader interface {
	LoadUpgradeCandidate(context.Context, string, int) (UpgradeCandidate, error)
}

type InstalledBinaryVerifier interface {
	VerifyInstalledBinary(context.Context, string, string) error
}

type Service struct {
	Paths                layout.Paths
	UID                  int
	Target               install.Target
	Ledger               install.Ledger
	Secrets              secret.Store
	BlockingEvents       BlockingEventStore
	HermesEndpoint       string
	HermesListenAddress  string
	HermesGrantToken     []byte
	IdentityKey          []byte
	ControlPlaneStates   ControlPlaneStateStore
	ControlProvisioner   ControlPlaneProvisioner
	ProviderBackend      ProviderBackendUninstaller
	ControlRequest       controlplane.ProvisionRequest
	PanelRuntime         *panel.Runtime
	PanelDesired         panel.Desired
	PanelConnectionID    string
	PrincipalAgentStates PrincipalAgentStateStore
	JournalMaintenance   JournalMaintenanceStore
	OperatorID           string
	HermesConfigPath     string
	Restarter            MaintenanceRestarter
	HermesGrantVerifier  HermesGrantVerifier
	RandomSource         io.Reader
	ActiveSchema         int
	UpgradeCandidates    UpgradeCandidateLoader
	InstalledVerifier    InstalledBinaryVerifier
}
