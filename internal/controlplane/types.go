package controlplane

import "mlink/internal/journal"

const (
	AdminUserKeyAccount = "control/tencentdb/admin-user-key"
	OwnerUserKeyAccount = "control/tencentdb/owner-user-key"
	PanelContainerName  = "mlink-memory-panel"
	PanelImageName      = "mlink-memory-panel:a5dcbe6"
)

type ProvisionRequest struct {
	InstallationID string
	InstanceID     string
	AdminUsername  string
	OwnerUsername  string
	TeamName       string
	OwnerAgentName string
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
