package app

import (
	"context"

	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/panel"
	"mlink/internal/secret"
)

type Agent string

const (
	Codex  Agent = "codex"
	Pi     Agent = "pi"
	Hermes Agent = "hermes"
)

const (
	MemoryCoreTokenSecret = "memorycore_token"
	OwnerBindingSecret    = "owner_feishu_binding"
)

type InstallRequest struct {
	Agents           []Agent
	Connection       config.Connection
	OwnerSlug        string
	OwnerBindingSlot config.BindingRef
	SecretInputs     map[string][]byte
	HermesMachine    string
	HermesHome       string
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

type PrincipalAgentStateStore interface {
	GetPrincipalAgent(context.Context, string) (journal.PrincipalAgent, error)
	PutPrincipalAgent(context.Context, journal.PrincipalAgent) error
	ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error)
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
	ControlProvisioner   *controlplane.Service
	ControlRequest       controlplane.ProvisionRequest
	PanelRuntime         *panel.Runtime
	PanelDesired         panel.Desired
	PanelConnectionID    string
	PrincipalAgentStates PrincipalAgentStateStore
}
