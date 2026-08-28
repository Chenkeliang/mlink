package app

import (
	"context"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/secret"
)

type Agent string

const (
	Codex  Agent = "codex"
	Pi     Agent = "pi"
	Hermes Agent = "hermes"
)

const MemoryCoreTokenSecret = "memorycore_token"

type InstallRequest struct {
	Agents        []Agent
	Connection    config.Connection
	SecretInputs  map[string][]byte
	HermesMachine string
	HermesHome    string
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

type Service struct {
	Paths               layout.Paths
	UID                 int
	Target              install.Target
	Ledger              install.Ledger
	Secrets             secret.Store
	BlockingEvents      BlockingEventStore
	HermesEndpoint      string
	HermesListenAddress string
	HermesGrantToken    []byte
	IdentityKey         []byte
}
