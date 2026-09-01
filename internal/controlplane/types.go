package controlplane

import (
	"mlink/internal/journal"
	"mlink/internal/panel"
)

const (
	AdminUserKeyAccount = "control/tencentdb/admin-user-key"
	OwnerUserKeyAccount = "control/tencentdb/owner-user-key"
	PanelContainerName  = panel.ContainerName
	PanelImageName      = panel.ImageReference
)

type ProvisionRequest struct {
	InstallationID    string
	InstanceID        string
	ConnectionID      string
	ProviderEndpoint  string
	AdminUsername     string
	OwnerUsername     string
	TeamName          string
	OwnerAgentName    string
	DynamicAgentLimit int
}

type ProvisionResult struct {
	PlanID       string
	OwnerUserID  string
	OwnerTeamID  string
	OwnerAgentID string
	OwnerAssetID string
}

func resultFromState(planID string, state journal.ControlPlaneState) ProvisionResult {
	return ProvisionResult{
		PlanID: planID, OwnerUserID: state.OwnerUserID, OwnerTeamID: state.OwnerTeamID,
		OwnerAgentID: state.OwnerAgentID, OwnerAssetID: state.OwnerAssetID,
	}
}
