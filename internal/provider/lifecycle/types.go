package lifecycle

import (
	"context"
	"fmt"

	"mlink/internal/config"
	"mlink/internal/install"
)

type BackendState string

const (
	BackendReachable         BackendState = "reachable"
	BackendStopped           BackendState = "stopped"
	BackendAbsent            BackendState = "absent"
	BackendIncompatible      BackendState = "incompatible"
	BackendRemoteUnreachable BackendState = "remote_unreachable"
)

type BackendStatus struct {
	ProviderID string       `json:"provider_id"`
	State      BackendState `json:"state"`
	Endpoint   string       `json:"endpoint"`
	Version    string       `json:"version,omitempty"`
	Local      bool         `json:"local"`
	Installed  bool         `json:"installed"`
}

type BackendInstallRequest struct {
	ProviderID   string
	Endpoint     string
	GatewayToken []byte
	LLMBaseURL   string
	LLMModel     string
	LLMAPIKey    []byte
}

func (request BackendInstallRequest) String() string {
	return fmt.Sprintf("BackendInstallRequest{ProviderID:%q Endpoint:%q GatewayToken:<redacted> LLMBaseURL:%q LLMModel:%q LLMAPIKey:<redacted>}", request.ProviderID, request.Endpoint, request.LLMBaseURL, request.LLMModel)
}

func (request BackendInstallRequest) GoString() string { return request.String() }

func (request *BackendInstallRequest) Wipe() {
	for index := range request.GatewayToken {
		request.GatewayToken[index] = 0
	}
	for index := range request.LLMAPIKey {
		request.LLMAPIKey[index] = 0
	}
}

type BackendLifecycle interface {
	Detect(context.Context, config.Connection) (BackendStatus, error)
	PlanInstall(context.Context, BackendInstallRequest) (install.ChangeSet, error)
	ApplyInstall(context.Context, string, BackendInstallRequest) error
}
