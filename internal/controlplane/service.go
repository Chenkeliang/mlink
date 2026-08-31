package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/secret"
)

type StateStore interface {
	SaveControlPlane(context.Context, journal.ControlPlaneState) error
	LoadControlPlane(context.Context) (journal.ControlPlaneState, error)
}

type Service struct {
	Metadata tencentdb.MetadataClient
	Secrets  secret.Store
	States   StateStore
}

func (service Service) PlanProvision(ctx context.Context, request ProvisionRequest) (install.ChangeSet, error) {
	if err := validateProvisionRequest(request); err != nil {
		return install.ChangeSet{}, err
	}
	if service.States == nil {
		return install.ChangeSet{}, errors.New("control-plane state store is required")
	}
	mode := "create"
	state, err := service.States.LoadControlPlane(ctx)
	if err == nil {
		if state.InstallationID != request.InstallationID || state.InstanceID != request.InstanceID {
			return install.ChangeSet{}, errors.New("existing control plane belongs to another installation")
		}
		mode = "reuse"
	} else if !errors.Is(err, fs.ErrNotExist) {
		return install.ChangeSet{}, err
	}
	resources := []install.DesiredResource{
		provisionIntent(request, mode, "system-admin", "system administrator", "Core-generated system_admin and Keychain credential"),
		provisionIntent(request, mode, "owner-user", "normal Owner user", "Core-generated normal user and Keychain credential"),
		provisionIntent(request, mode, "owner-team", "Owner team", "Core-generated Team owned by the normal user"),
		provisionIntent(request, mode, "owner-agent", "Owner Agent and Asset", "Core-generated Agent with auto-minted chat_memory Asset"),
	}
	plan, err := install.BuildChangeSet(intentTarget{}, resources)
	if err != nil {
		return install.ChangeSet{}, err
	}
	plan.SelectedConnection = request.InstanceID
	return plan, nil
}

func (service Service) ApplyProvision(ctx context.Context, planID string, request ProvisionRequest) (ProvisionResult, error) {
	if service.Metadata == nil || service.Secrets == nil || service.States == nil {
		return ProvisionResult{}, errors.New("metadata, Keychain, and state store are required")
	}
	plan, err := service.PlanProvision(ctx, request)
	if err != nil {
		return ProvisionResult{}, err
	}
	if plan.PlanID != planID {
		return ProvisionResult{}, fmt.Errorf("%w: control-plane plan changed", install.ErrPlanStale)
	}
	if state, err := service.States.LoadControlPlane(ctx); err == nil {
		return resultFromState(planID, state), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ProvisionResult{}, err
	}

	admin, adminKey, err := service.ensureAdmin(ctx, request)
	if err != nil {
		return ProvisionResult{}, err
	}
	defer wipe(adminKey)
	owner, ownerKey, err := service.ensureOwner(ctx, request, adminKey)
	if err != nil {
		return ProvisionResult{}, err
	}
	defer wipe(ownerKey)
	if admin.UserID == owner.UserID || admin.UserType != "system_admin" || owner.UserType != "normal" {
		return ProvisionResult{}, errors.New("TencentDB admin and Owner identities are invalid")
	}

	teamMarker := metadataMarker(request.InstallationID, "owner-team")
	team, err := service.ensureTeam(ctx, request, owner, ownerKey, teamMarker)
	if err != nil {
		return ProvisionResult{}, err
	}
	agentMarker := metadataMarker(request.InstallationID, "owner-agent")
	agent, err := service.ensureAgent(ctx, request, owner, ownerKey, team, agentMarker)
	if err != nil {
		return ProvisionResult{}, err
	}
	assetID := "chat_memory-" + team.TeamID + "-" + agent.AgentID
	asset, err := service.Metadata.GetAsset(ctx, ownerKey, assetID)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("load Owner Chat Memory Asset: %w", err)
	}
	if asset.AssetID != assetID || asset.TeamID != team.TeamID || asset.OwnerUserID != owner.UserID || asset.AssetType != "chat_memory" {
		return ProvisionResult{}, errors.New("TencentDB returned an invalid Owner Chat Memory Asset")
	}

	state := journal.ControlPlaneState{
		InstallationID: request.InstallationID, InstanceID: request.InstanceID,
		OwnerUserID: owner.UserID, OwnerTeamID: team.TeamID, OwnerAgentID: agent.AgentID, OwnerAssetID: asset.AssetID,
		PanelContainer: PanelContainerName, PanelImage: PanelImageName, State: "provisioned",
	}
	if err := service.States.SaveControlPlane(ctx, state); err != nil {
		return ProvisionResult{}, fmt.Errorf("save provisioned control plane: %w", err)
	}
	return resultFromState(planID, state), nil
}

func (service Service) ensureAdmin(ctx context.Context, request ProvisionRequest) (tencentdb.User, []byte, error) {
	key, err := service.Secrets.Get(ctx, AdminUserKeyAccount)
	if err == nil {
		user, verifyErr := service.Metadata.VerifyUser(ctx, key)
		if verifyErr != nil || user.UserType != "system_admin" {
			wipe(key)
			return tencentdb.User{}, nil, errors.New("stored TencentDB admin credential is invalid")
		}
		return user, key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tencentdb.User{}, nil, err
	}
	credential, err := service.Metadata.InitAdmin(ctx, tencentdb.InitAdminRequest{Username: request.AdminUsername})
	if err != nil {
		return tencentdb.User{}, nil, errors.New("initialize TencentDB metadata administrator")
	}
	defer credential.Wipe()
	if err := service.Secrets.Put(ctx, AdminUserKeyAccount, credential.UserKey); err != nil {
		return tencentdb.User{}, nil, errors.New("store TencentDB admin credential")
	}
	key = append([]byte(nil), credential.UserKey...)
	user, err := service.Metadata.VerifyUser(ctx, key)
	if err != nil || user.UserID != credential.UserID || user.UserType != "system_admin" {
		wipe(key)
		return tencentdb.User{}, nil, errors.New("verify TencentDB metadata administrator")
	}
	return user, key, nil
}

func (service Service) ensureOwner(ctx context.Context, request ProvisionRequest, adminKey []byte) (tencentdb.User, []byte, error) {
	key, err := service.Secrets.Get(ctx, OwnerUserKeyAccount)
	if err == nil {
		user, verifyErr := service.Metadata.VerifyUser(ctx, key)
		if verifyErr != nil || user.UserType != "normal" || user.Username != request.OwnerUsername {
			wipe(key)
			return tencentdb.User{}, nil, errors.New("stored TencentDB Owner credential is invalid")
		}
		return user, key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tencentdb.User{}, nil, err
	}
	credential, err := service.Metadata.CreateUser(ctx, adminKey, tencentdb.CreateUserRequest{Username: request.OwnerUsername})
	if err != nil {
		return tencentdb.User{}, nil, errors.New("create TencentDB Owner user")
	}
	defer credential.Wipe()
	if err := service.Secrets.Put(ctx, OwnerUserKeyAccount, credential.UserKey); err != nil {
		return tencentdb.User{}, nil, errors.New("store TencentDB Owner credential")
	}
	key = append([]byte(nil), credential.UserKey...)
	user, err := service.Metadata.VerifyUser(ctx, key)
	if err != nil || user.UserID != credential.UserID || user.UserType != "normal" {
		wipe(key)
		return tencentdb.User{}, nil, errors.New("verify TencentDB Owner user")
	}
	return user, key, nil
}

func (service Service) ensureTeam(ctx context.Context, request ProvisionRequest, owner tencentdb.User, ownerKey []byte, marker string) (tencentdb.Team, error) {
	teams, err := service.Metadata.ListTeams(ctx, ownerKey, tencentdb.ListTeamsRequest{UserID: owner.UserID, Name: request.TeamName, Limit: 100})
	if err != nil {
		return tencentdb.Team{}, fmt.Errorf("list TencentDB Owner Teams: %w", err)
	}
	for _, team := range teams {
		if team.Name == request.TeamName {
			if team.OwnerUserID != owner.UserID || team.MetadataJSON != marker {
				return tencentdb.Team{}, errors.New("TencentDB Owner Team name is already used by another resource")
			}
			return team, nil
		}
	}
	team, err := service.Metadata.CreateTeam(ctx, ownerKey, tencentdb.CreateTeamRequest{
		Name: request.TeamName, OwnerUserID: owner.UserID, Description: "MLink managed memory Team", MetadataJSON: marker,
	})
	if err != nil {
		return tencentdb.Team{}, fmt.Errorf("create TencentDB Owner Team: %w", err)
	}
	if team.OwnerUserID != owner.UserID || team.MetadataJSON != marker {
		return tencentdb.Team{}, errors.New("TencentDB returned an invalid Owner Team")
	}
	return team, nil
}

func (service Service) ensureAgent(ctx context.Context, request ProvisionRequest, owner tencentdb.User, ownerKey []byte, team tencentdb.Team, marker string) (tencentdb.Agent, error) {
	agents, err := service.Metadata.ListAgents(ctx, ownerKey, tencentdb.ListAgentsRequest{
		TeamID: team.TeamID, OwnerUserID: owner.UserID, Name: request.OwnerAgentName, Limit: 100,
	})
	if err != nil {
		return tencentdb.Agent{}, fmt.Errorf("list TencentDB Owner Agents: %w", err)
	}
	for _, agent := range agents {
		if agent.Name == request.OwnerAgentName {
			if agent.TeamID != team.TeamID || agent.OwnerUserID != owner.UserID || agent.MetadataJSON != marker {
				return tencentdb.Agent{}, errors.New("TencentDB Owner Agent name is already used by another resource")
			}
			return agent, nil
		}
	}
	agent, err := service.Metadata.CreateAgent(ctx, ownerKey, tencentdb.CreateAgentRequest{
		TeamID: team.TeamID, OwnerUserID: owner.UserID, Name: request.OwnerAgentName,
		Description: "MLink Owner memory", Visibility: "private", MetadataJSON: marker,
	})
	if err != nil {
		return tencentdb.Agent{}, fmt.Errorf("create TencentDB Owner Agent: %w", err)
	}
	if agent.TeamID != team.TeamID || agent.OwnerUserID != owner.UserID || agent.MetadataJSON != marker {
		return tencentdb.Agent{}, errors.New("TencentDB returned an invalid Owner Agent")
	}
	return agent, nil
}

func provisionIntent(request ProvisionRequest, mode, suffix, before, after string) install.DesiredResource {
	return install.DesiredResource{
		OwnerID: "dev.mlink.control-plane", Target: "remote:tencentdb:" + request.InstanceID + ":" + mode + ":" + suffix,
		Action: install.ActionService, Command: []string{"mlink-internal", "provision-control-plane", suffix},
		SemanticDiff: []install.SemanticDiff{{Path: "control-plane:" + suffix, Before: before, After: after}},
	}
}

func metadataMarker(installationID, role string) string {
	return fmt.Sprintf(`{"mlink":{"installation_id":%q,"role":%q,"schema_version":1}}`, installationID, role)
}

func validateProvisionRequest(request ProvisionRequest) error {
	for _, value := range []string{request.InstallationID, request.InstanceID, request.AdminUsername, request.OwnerUsername, request.TeamName, request.OwnerAgentName} {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return errors.New("complete bounded control-plane provisioning request is required")
		}
	}
	if request.AdminUsername == request.OwnerUsername {
		return errors.New("TencentDB admin and Owner usernames must differ")
	}
	return nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type intentTarget struct{}

func (intentTarget) Read(context.Context, string) ([]byte, fs.FileMode, error) {
	return nil, 0, fs.ErrNotExist
}
func (intentTarget) WriteAtomic(context.Context, string, []byte, fs.FileMode) error {
	return errors.New("intent target is read-only")
}
func (intentTarget) Remove(context.Context, string) error {
	return errors.New("intent target is read-only")
}
func (intentTarget) Run(context.Context, []string, io.Reader) ([]byte, error) {
	return nil, errors.New("intent target does not execute commands")
}
