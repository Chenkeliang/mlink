package controlplane

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"mlink/internal/journal"
	"mlink/internal/provider/tencentdb"
)

type fakeMetadata struct {
	initialized bool
	admin       tencentdb.User
	owner       tencentdb.User
	team        tencentdb.Team
	agent       tencentdb.Agent
	asset       tencentdb.Asset
	initCalls   int
	userCalls   int
	teamCalls   int
	agentCalls  int
}

func (metadata *fakeMetadata) InitAdmin(_ context.Context, request tencentdb.InitAdminRequest) (tencentdb.UserCredential, error) {
	metadata.initCalls++
	if metadata.initialized {
		return tencentdb.UserCredential{}, errors.New("already initialized")
	}
	metadata.initialized = true
	metadata.admin = tencentdb.User{UserID: "usr-admin", UserType: "system_admin", Username: request.Username}
	return tencentdb.UserCredential{UserID: metadata.admin.UserID, UserKey: []byte("admin-key")}, nil
}

func (metadata *fakeMetadata) CreateUser(_ context.Context, _ []byte, request tencentdb.CreateUserRequest) (tencentdb.UserCredential, error) {
	metadata.userCalls++
	metadata.owner = tencentdb.User{UserID: "usr-owner", UserType: "normal", Username: request.Username}
	return tencentdb.UserCredential{UserID: metadata.owner.UserID, UserKey: []byte("owner-key")}, nil
}

func (metadata *fakeMetadata) VerifyUser(_ context.Context, key []byte) (tencentdb.User, error) {
	switch string(key) {
	case "admin-key":
		if metadata.admin.UserID == "" {
			metadata.admin = tencentdb.User{UserID: "usr-admin", UserType: "system_admin", Username: "mlink-admin"}
		}
		return metadata.admin, nil
	case "owner-key":
		if metadata.owner.UserID == "" {
			metadata.owner = tencentdb.User{UserID: "usr-owner", UserType: "normal", Username: "keliang"}
		}
		return metadata.owner, nil
	default:
		return tencentdb.User{}, errors.New("invalid key")
	}
}

func (metadata *fakeMetadata) CreateTeam(_ context.Context, _ []byte, request tencentdb.CreateTeamRequest) (tencentdb.Team, error) {
	metadata.teamCalls++
	metadata.team = tencentdb.Team{TeamID: "team-owner", Name: request.Name, OwnerUserID: request.OwnerUserID, MetadataJSON: request.MetadataJSON, Status: "active"}
	return metadata.team, nil
}

func (metadata *fakeMetadata) ListTeams(context.Context, []byte, tencentdb.ListTeamsRequest) ([]tencentdb.Team, error) {
	if metadata.team.TeamID == "" {
		return nil, nil
	}
	return []tencentdb.Team{metadata.team}, nil
}

func (metadata *fakeMetadata) CreateAgent(_ context.Context, _ []byte, request tencentdb.CreateAgentRequest) (tencentdb.Agent, error) {
	metadata.agentCalls++
	metadata.agent = tencentdb.Agent{AgentID: "agt-owner", TeamID: request.TeamID, OwnerUserID: request.OwnerUserID, Name: request.Name, MetadataJSON: request.MetadataJSON, Status: "active", Visibility: request.Visibility}
	metadata.asset = tencentdb.Asset{AssetID: "chat_memory-team-owner-agt-owner", TeamID: request.TeamID, OwnerUserID: request.OwnerUserID, AssetType: "chat_memory", Status: "approved"}
	return metadata.agent, nil
}

func (metadata *fakeMetadata) ListAgents(context.Context, []byte, tencentdb.ListAgentsRequest) ([]tencentdb.Agent, error) {
	if metadata.agent.AgentID == "" {
		return nil, nil
	}
	return []tencentdb.Agent{metadata.agent}, nil
}

func (metadata *fakeMetadata) GetAsset(context.Context, []byte, string) (tencentdb.Asset, error) {
	if metadata.asset.AssetID == "" {
		return tencentdb.Asset{}, errors.New("asset missing")
	}
	return metadata.asset, nil
}

func (*fakeMetadata) InstanceQuota(context.Context, []byte) (tencentdb.InstanceQuota, error) {
	return tencentdb.InstanceQuota{MaxUsers: 100, MaxTeams: 20}, nil
}

type fakeSecrets struct {
	values map[string][]byte
	puts   int
}

func (store *fakeSecrets) Put(_ context.Context, account string, value []byte) error {
	store.puts++
	if store.values == nil {
		store.values = map[string][]byte{}
	}
	store.values[account] = append([]byte(nil), value...)
	return nil
}

func (store *fakeSecrets) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store.values[account]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}

func (store *fakeSecrets) Delete(_ context.Context, account string) error {
	delete(store.values, account)
	return nil
}

type fakeStateStore struct {
	value    journal.ControlPlaneState
	saves    int
	failOnce bool
}

func (store *fakeStateStore) SaveControlPlane(_ context.Context, value journal.ControlPlaneState) error {
	store.saves++
	if store.failOnce {
		store.failOnce = false
		return errors.New("journal unavailable")
	}
	store.value = value
	return nil
}

func (store *fakeStateStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	if store.value.InstallationID == "" {
		return journal.ControlPlaneState{}, fs.ErrNotExist
	}
	return store.value, nil
}

func TestProvisionPreviewPerformsZeroWrites(t *testing.T) {
	metadata := &fakeMetadata{}
	secrets := &fakeSecrets{values: map[string][]byte{}}
	states := &fakeStateStore{}
	service := Service{Metadata: metadata, Secrets: secrets, States: states}
	plan, err := service.PlanProvision(context.Background(), fixtureProvisionRequest())
	if err != nil || plan.PlanID == "" || len(plan.Operations) != 4 {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if metadata.initCalls+metadata.userCalls+metadata.teamCalls+metadata.agentCalls != 0 || secrets.puts != 0 || states.saves != 0 {
		t.Fatalf("preview writes metadata=%#v secrets=%d states=%d", metadata, secrets.puts, states.saves)
	}
	for _, operation := range plan.Operations {
		if strings.Contains(operation.Target, "admin-key") || strings.Contains(operation.Target, "owner-key") {
			t.Fatalf("secret leaked into plan: %#v", operation)
		}
	}
}

func TestApplyProvisionCreatesDistinctAdminAndOwner(t *testing.T) {
	metadata := &fakeMetadata{}
	secrets := &fakeSecrets{values: map[string][]byte{}}
	states := &fakeStateStore{}
	service := Service{Metadata: metadata, Secrets: secrets, States: states}
	request := fixtureProvisionRequest()
	plan, _ := service.PlanProvision(context.Background(), request)
	result, err := service.ApplyProvision(context.Background(), plan.PlanID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.OwnerUserID != "usr-owner" || result.OwnerTeamID != "team-owner" || result.OwnerAgentID != "agt-owner" || result.OwnerAssetID == "" {
		t.Fatalf("result = %#v", result)
	}
	if string(secrets.values[AdminUserKeyAccount]) != "admin-key" || string(secrets.values[OwnerUserKeyAccount]) != "owner-key" {
		t.Fatalf("secret accounts = %#v", secrets.values)
	}
	if metadata.admin.UserID == metadata.owner.UserID || metadata.admin.UserType != "system_admin" || metadata.owner.UserType != "normal" {
		t.Fatalf("admin/owner = %#v/%#v", metadata.admin, metadata.owner)
	}
	if states.value.State != "provisioned" || states.value.OwnerAgentID != result.OwnerAgentID || states.value.PanelContainer != "tdai-memory-hub" || states.value.PanelImage != "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104" {
		t.Fatalf("state = %#v", states.value)
	}
}

func TestApplyProvisionReusesExistingStateWithoutWrites(t *testing.T) {
	request := fixtureProvisionRequest()
	state := fixtureControlPlaneState()
	states := &fakeStateStore{value: state}
	metadata := &fakeMetadata{
		owner: tencentdb.User{UserID: state.OwnerUserID, UserType: "normal", Username: "keliang"},
		team:  tencentdb.Team{TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID},
		agent: tencentdb.Agent{AgentID: state.OwnerAgentID, TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID},
		asset: tencentdb.Asset{AssetID: state.OwnerAssetID, TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, AssetType: "chat_memory"},
	}
	secrets := &fakeSecrets{values: map[string][]byte{OwnerUserKeyAccount: []byte("owner-key")}}
	service := Service{Metadata: metadata, Secrets: secrets, States: states}
	plan, _ := service.PlanProvision(context.Background(), request)
	result, err := service.ApplyProvision(context.Background(), plan.PlanID, request)
	if err != nil || result.OwnerAgentID != "agt-owner" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if metadata.initCalls+metadata.userCalls+metadata.teamCalls+metadata.agentCalls != 0 || secrets.puts != 0 || states.saves != 0 {
		t.Fatalf("reuse wrote metadata=%#v secrets=%d states=%d", metadata, secrets.puts, states.saves)
	}
}

func TestApplyProvisionRejectsExistingIdentityFromDifferentBackend(t *testing.T) {
	request := fixtureProvisionRequest()
	state := fixtureControlPlaneState()
	states := &fakeStateStore{value: state}
	metadata := &fakeMetadata{owner: tencentdb.User{UserID: "usr-other", UserType: "normal", Username: "keliang"}}
	secrets := &fakeSecrets{values: map[string][]byte{OwnerUserKeyAccount: []byte("owner-key")}}
	service := Service{Metadata: metadata, Secrets: secrets, States: states}
	plan, err := service.PlanProvision(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProvision(context.Background(), plan.PlanID, request); err == nil || !strings.Contains(err.Error(), "existing Core identity") {
		t.Fatalf("ApplyProvision() error = %v", err)
	}
}

func TestPlanProvisionRejectsCapacityChangeForExistingCoreIdentity(t *testing.T) {
	states := &fakeStateStore{value: fixtureControlPlaneState()}
	service := Service{Metadata: &fakeMetadata{}, Secrets: &fakeSecrets{values: map[string][]byte{}}, States: states}
	request := fixtureProvisionRequest()
	request.DynamicAgentLimit = states.value.DynamicAgentLimit + 1
	if _, err := service.PlanProvision(context.Background(), request); err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("PlanProvision() error = %v", err)
	}
}

func TestApplyProvisionInitializedBackendWithoutCredentialsFailsClosed(t *testing.T) {
	metadata := &fakeMetadata{initialized: true}
	service := Service{Metadata: metadata, Secrets: &fakeSecrets{values: map[string][]byte{}}, States: &fakeStateStore{}}
	request := fixtureProvisionRequest()
	plan, _ := service.PlanProvision(context.Background(), request)
	if _, err := service.ApplyProvision(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("ApplyProvision() error = nil")
	}
	if metadata.userCalls+metadata.teamCalls+metadata.agentCalls != 0 {
		t.Fatalf("metadata mutated after missing admin credential: %#v", metadata)
	}
}

func TestApplyProvisionRecoversRemoteCreateAfterJournalFailure(t *testing.T) {
	metadata := &fakeMetadata{}
	secrets := &fakeSecrets{values: map[string][]byte{}}
	states := &fakeStateStore{failOnce: true}
	service := Service{Metadata: metadata, Secrets: secrets, States: states}
	request := fixtureProvisionRequest()
	plan, _ := service.PlanProvision(context.Background(), request)
	if _, err := service.ApplyProvision(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("first ApplyProvision() error = nil")
	}
	result, err := service.ApplyProvision(context.Background(), plan.PlanID, request)
	if err != nil || result.OwnerAgentID != "agt-owner" {
		t.Fatalf("retry = %#v, %v", result, err)
	}
	if metadata.initCalls != 1 || metadata.userCalls != 1 || metadata.teamCalls != 1 || metadata.agentCalls != 1 {
		t.Fatalf("remote resources duplicated: %#v", metadata)
	}
}

func fixtureProvisionRequest() ProvisionRequest {
	return ProvisionRequest{
		InstallationID: "installation-1", InstanceID: "default", AdminUsername: "mlink-admin",
		OwnerUsername: "keliang", TeamName: "MLink", OwnerAgentName: "MLink Owner",
	}
}

func fixtureControlPlaneState() journal.ControlPlaneState {
	return journal.ControlPlaneState{
		InstallationID: "installation-1", InstanceID: "default", DynamicAgentLimit: 500, OwnerUserID: "usr-owner",
		OwnerTeamID: "team-owner", OwnerAgentID: "agt-owner", OwnerAssetID: "chat_memory-team-owner-agt-owner",
		PanelContainer: "tdai-memory-hub", PanelImage: "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104", State: "provisioned",
	}
}
