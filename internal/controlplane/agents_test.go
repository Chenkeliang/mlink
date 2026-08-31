package controlplane

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"mlink/internal/journal"
	"mlink/internal/provider/tencentdb"
)

type agentStore struct {
	mu       sync.Mutex
	state    journal.ControlPlaneState
	mappings map[string]journal.PrincipalAgent
	putCalls int
	failPut  bool
}

func (store *agentStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	if store.state.InstallationID == "" {
		return journal.ControlPlaneState{}, fs.ErrNotExist
	}
	return store.state, nil
}

func (store *agentStore) GetPrincipalAgent(_ context.Context, fingerprint string) (journal.PrincipalAgent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.mappings[fingerprint]
	if !ok {
		return journal.PrincipalAgent{}, journal.ErrPrincipalAgentNotFound
	}
	return value, nil
}

func (store *agentStore) PutPrincipalAgent(_ context.Context, value journal.PrincipalAgent) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.putCalls++
	if store.failPut {
		store.failPut = false
		return errors.New("journal unavailable")
	}
	if store.mappings == nil {
		store.mappings = map[string]journal.PrincipalAgent{}
	}
	store.mappings[value.Fingerprint] = value
	return nil
}

func (store *agentStore) ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	values := make([]journal.PrincipalAgent, 0, len(store.mappings))
	for _, value := range store.mappings {
		values = append(values, value)
	}
	return values, nil
}

type dynamicMetadata struct {
	mu            sync.Mutex
	agents        []tencentdb.Agent
	assets        map[string]tencentdb.Asset
	createCalls   int
	lastCreate    tencentdb.CreateAgentRequest
	createEntered chan struct{}
	createRelease chan struct{}
	listBlock     chan struct{}
	wrongCreate   bool
}

func (*dynamicMetadata) InitAdmin(context.Context, tencentdb.InitAdminRequest) (tencentdb.UserCredential, error) {
	return tencentdb.UserCredential{}, errors.New("unused")
}
func (*dynamicMetadata) CreateUser(context.Context, []byte, tencentdb.CreateUserRequest) (tencentdb.UserCredential, error) {
	return tencentdb.UserCredential{}, errors.New("unused")
}
func (*dynamicMetadata) VerifyUser(context.Context, []byte) (tencentdb.User, error) {
	return tencentdb.User{}, errors.New("unused")
}
func (*dynamicMetadata) CreateTeam(context.Context, []byte, tencentdb.CreateTeamRequest) (tencentdb.Team, error) {
	return tencentdb.Team{}, errors.New("unused")
}
func (*dynamicMetadata) ListTeams(context.Context, []byte, tencentdb.ListTeamsRequest) ([]tencentdb.Team, error) {
	return nil, errors.New("unused")
}
func (metadata *dynamicMetadata) CreateAgent(ctx context.Context, _ []byte, request tencentdb.CreateAgentRequest) (tencentdb.Agent, error) {
	metadata.mu.Lock()
	metadata.createCalls++
	metadata.lastCreate = request
	entered := metadata.createEntered
	release := metadata.createRelease
	metadata.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return tencentdb.Agent{}, ctx.Err()
		}
	}
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	index := metadata.createCalls
	agent := tencentdb.Agent{
		AgentID: "agt-dynamic-" + string(rune('0'+index)), TeamID: request.TeamID,
		OwnerUserID: request.OwnerUserID, Name: request.Name, Visibility: request.Visibility,
		Status: "active", MetadataJSON: request.MetadataJSON,
	}
	if metadata.wrongCreate {
		agent.TeamID = "team-wrong"
	}
	metadata.agents = append(metadata.agents, agent)
	if metadata.assets == nil {
		metadata.assets = map[string]tencentdb.Asset{}
	}
	assetID := "chat_memory-" + agent.TeamID + "-" + agent.AgentID
	metadata.assets[assetID] = tencentdb.Asset{
		AssetID: assetID, TeamID: agent.TeamID, OwnerUserID: agent.OwnerUserID,
		AssetType: "chat_memory", Status: "approved",
	}
	return agent, nil
}
func (metadata *dynamicMetadata) ListAgents(ctx context.Context, _ []byte, request tencentdb.ListAgentsRequest) ([]tencentdb.Agent, error) {
	if metadata.listBlock != nil {
		select {
		case <-metadata.listBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	if request.Offset >= len(metadata.agents) {
		return nil, nil
	}
	end := request.Offset + request.Limit
	if request.Limit == 0 || end > len(metadata.agents) {
		end = len(metadata.agents)
	}
	return append([]tencentdb.Agent(nil), metadata.agents[request.Offset:end]...), nil
}
func (metadata *dynamicMetadata) GetAsset(_ context.Context, _ []byte, assetID string) (tencentdb.Asset, error) {
	metadata.mu.Lock()
	defer metadata.mu.Unlock()
	asset, ok := metadata.assets[assetID]
	if !ok {
		return tencentdb.Asset{}, fs.ErrNotExist
	}
	return asset, nil
}
func (*dynamicMetadata) InstanceQuota(context.Context, []byte) (tencentdb.InstanceQuota, error) {
	return tencentdb.InstanceQuota{MaxUsers: 100, MaxTeams: 20}, nil
}

func TestAgentProvisionerReturnsExistingMappingWithoutRemoteMutation(t *testing.T) {
	intent := fixturePrincipalIntent()
	existing := fixturePrincipalAgent(intent)
	store := fixtureAgentStore()
	store.mappings[intent.Fingerprint] = existing
	metadata := &dynamicMetadata{}
	provisioner := fixtureAgentProvisioner(store, metadata)

	got, err := provisioner.ResolveOrCreate(context.Background(), intent)
	if err != nil || got != existing {
		t.Fatalf("mapping = %#v, %v", got, err)
	}
	if metadata.createCalls != 0 || store.putCalls != 0 {
		t.Fatalf("writes = metadata:%d journal:%d", metadata.createCalls, store.putCalls)
	}
}

func TestAgentProvisionerReconcilesRemoteMarkerBeforeCreate(t *testing.T) {
	intent := fixturePrincipalIntent()
	state := fixtureControlPlaneState()
	marker := principalMetadataMarker(state.InstallationID, intent)
	agent := tencentdb.Agent{AgentID: "agt-remote", TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, Name: "existing", Status: "active", MetadataJSON: marker}
	assetID := "chat_memory-" + state.OwnerTeamID + "-" + agent.AgentID
	metadata := &dynamicMetadata{agents: []tencentdb.Agent{agent}, assets: map[string]tencentdb.Asset{assetID: {
		AssetID: assetID, TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, AssetType: "chat_memory", Status: "approved",
	}}}
	store := fixtureAgentStore()
	store.mappings[intent.Fingerprint] = journal.PrincipalAgent{
		Fingerprint: intent.Fingerprint, RouteKind: intent.RouteKind, BackendUserID: state.OwnerUserID,
		BackendTeamID: state.OwnerTeamID, BackendAgentID: agent.AgentID, BackendAssetID: assetID,
		DisplayLabel: intent.DisplayLabel, State: "provisioning",
	}
	provisioner := fixtureAgentProvisioner(store, metadata)

	got, err := provisioner.ResolveOrCreate(context.Background(), intent)
	if err != nil || got.BackendAgentID != "agt-remote" || got.State != "active" || metadata.createCalls != 0 || store.putCalls != 1 {
		t.Fatalf("mapping/create/puts = %#v/%d/%d, %v", got, metadata.createCalls, store.putCalls, err)
	}
}

func TestAgentProvisionerCreatesOneAgentForConcurrentRequests(t *testing.T) {
	intent := fixturePrincipalIntent()
	store := fixtureAgentStore()
	metadata := &dynamicMetadata{createEntered: make(chan struct{}, 1), createRelease: make(chan struct{})}
	provisioner := fixtureAgentProvisioner(store, metadata)
	results := make(chan journal.PrincipalAgent, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			value, err := provisioner.ResolveOrCreate(context.Background(), intent)
			results <- value
			errors <- err
		}()
	}
	<-metadata.createEntered
	close(metadata.createRelease)
	first, second := <-results, <-results
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if metadata.createCalls != 1 || first.BackendAgentID == "" || first.BackendAgentID != second.BackendAgentID {
		t.Fatalf("create/results = %d/%#v/%#v", metadata.createCalls, first, second)
	}
}

func TestAgentProvisionerRejectsConflictingRouteDuringSingleFlight(t *testing.T) {
	intent := fixturePrincipalIntent()
	store := fixtureAgentStore()
	metadata := &dynamicMetadata{createEntered: make(chan struct{}, 1), createRelease: make(chan struct{})}
	provisioner := fixtureAgentProvisioner(store, metadata)
	firstDone := make(chan error, 1)
	go func() {
		_, err := provisioner.ResolveOrCreate(context.Background(), intent)
		firstDone <- err
	}()
	<-metadata.createEntered
	conflict := intent
	conflict.RouteKind = "hermes-group"
	conflictContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, conflictErr := provisioner.ResolveOrCreate(conflictContext, conflict)
	cancel()
	close(metadata.createRelease)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if conflictErr == nil || !strings.Contains(conflictErr.Error(), "conflicts") {
		t.Fatalf("conflict error = %v", conflictErr)
	}
	if metadata.createCalls != 1 {
		t.Fatalf("create calls = %d", metadata.createCalls)
	}
}

func TestAgentProvisionerFindsRemoteMarkerAfterFirstPage(t *testing.T) {
	intent := fixturePrincipalIntent()
	state := fixtureControlPlaneState()
	agents := make([]tencentdb.Agent, agentListPageSize)
	for index := range agents {
		agents[index] = tencentdb.Agent{AgentID: "unmanaged", TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID}
	}
	marker := principalMetadataMarker(state.InstallationID, intent)
	target := tencentdb.Agent{AgentID: "agt-page-two", TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, MetadataJSON: marker}
	agents = append(agents, target)
	assetID := "chat_memory-" + state.OwnerTeamID + "-" + target.AgentID
	metadata := &dynamicMetadata{agents: agents, assets: map[string]tencentdb.Asset{assetID: {
		AssetID: assetID, TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, AssetType: "chat_memory", Status: "approved",
	}}}

	got, err := fixtureAgentProvisioner(fixtureAgentStore(), metadata).ResolveOrCreate(context.Background(), intent)
	if err != nil || got.BackendAgentID != target.AgentID || metadata.createCalls != 0 {
		t.Fatalf("mapping/create = %#v/%d, %v", got, metadata.createCalls, err)
	}
}

func TestAgentProvisionerTimesOutFailClosed(t *testing.T) {
	store := fixtureAgentStore()
	metadata := &dynamicMetadata{listBlock: make(chan struct{})}
	provisioner := fixtureAgentProvisioner(store, metadata)
	provisioner.Timeout = 20 * time.Millisecond

	if _, err := provisioner.ResolveOrCreate(context.Background(), fixturePrincipalIntent()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if metadata.createCalls != 0 || store.putCalls != 0 {
		t.Fatalf("writes = metadata:%d journal:%d", metadata.createCalls, store.putCalls)
	}
}

func TestAgentProvisionerEnforcesExplicitDynamicAgentLimit(t *testing.T) {
	store := fixtureAgentStore()
	store.mappings["prn_bbbbbbbbbbbbbbbbbbbbbbbbbb"] = journal.PrincipalAgent{
		Fingerprint: "prn_bbbbbbbbbbbbbbbbbbbbbbbbbb", RouteKind: "hermes-private",
		BackendUserID: "usr-owner", BackendTeamID: "team-owner", BackendAgentID: "agt-existing",
		BackendAssetID: "chat_memory-team-owner-agt-existing", DisplayLabel: "existing", State: "active",
	}
	metadata := &dynamicMetadata{}
	provisioner := fixtureAgentProvisioner(store, metadata)
	provisioner.MaxDynamicAgents = 1

	if _, err := provisioner.ResolveOrCreate(context.Background(), fixturePrincipalIntent()); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v", err)
	}
	if metadata.createCalls != 0 {
		t.Fatalf("create calls = %d", metadata.createCalls)
	}
}

func TestAgentProvisionerRejectsWrongOwnerTeamAndDuplicateMarker(t *testing.T) {
	intent := fixturePrincipalIntent()
	store := fixtureAgentStore()
	wrong := &dynamicMetadata{wrongCreate: true}
	if _, err := fixtureAgentProvisioner(store, wrong).ResolveOrCreate(context.Background(), intent); err == nil {
		t.Fatal("wrong create response accepted")
	}

	state := fixtureControlPlaneState()
	marker := principalMetadataMarker(state.InstallationID, intent)
	duplicate := &dynamicMetadata{agents: []tencentdb.Agent{
		{AgentID: "agt-one", TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, MetadataJSON: marker},
		{AgentID: "agt-two", TeamID: state.OwnerTeamID, OwnerUserID: state.OwnerUserID, MetadataJSON: marker},
	}}
	if _, err := fixtureAgentProvisioner(fixtureAgentStore(), duplicate).ResolveOrCreate(context.Background(), intent); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestAgentProvisionerRecoversCreateAfterJournalFailure(t *testing.T) {
	intent := fixturePrincipalIntent()
	store := fixtureAgentStore()
	store.failPut = true
	metadata := &dynamicMetadata{}
	first := fixtureAgentProvisioner(store, metadata)
	if _, err := first.ResolveOrCreate(context.Background(), intent); err == nil {
		t.Fatal("first ResolveOrCreate() error = nil")
	}
	second := fixtureAgentProvisioner(store, metadata)
	got, err := second.ResolveOrCreate(context.Background(), intent)
	if err != nil || got.BackendAgentID == "" || metadata.createCalls != 1 {
		t.Fatalf("retry = %#v create=%d, %v", got, metadata.createCalls, err)
	}
}

func TestAgentProvisionerDoesNotPersistRawExternalIdentifiers(t *testing.T) {
	intent := fixturePrincipalIntent()
	store := fixtureAgentStore()
	metadata := &dynamicMetadata{}
	provisioner := fixtureAgentProvisioner(store, metadata)
	got, err := provisioner.ResolveOrCreate(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	combined := metadata.lastCreate.Name + metadata.lastCreate.MetadataJSON + got.DisplayLabel + got.Fingerprint
	for _, forbidden := range []string{"ou_", "oc_", "union_id", "open_id", "chat_id", "thread_id"} {
		if strings.Contains(strings.ToLower(combined), forbidden) {
			t.Fatalf("persisted forbidden identifier %q in %q", forbidden, combined)
		}
	}
	bad := intent
	bad.DisplayLabel = "ou_raw-external-id"
	if _, err := provisioner.ResolveOrCreate(context.Background(), bad); err == nil {
		t.Fatal("raw external identifier label accepted")
	}
}

func fixtureAgentProvisioner(store *agentStore, metadata *dynamicMetadata) *AgentProvisioner {
	return &AgentProvisioner{
		Metadata: metadata, Secrets: &fakeSecrets{values: map[string][]byte{OwnerUserKeyAccount: []byte("owner-key")}},
		Store: store, MaxDynamicAgents: 500, Timeout: time.Second,
	}
}

func fixtureAgentStore() *agentStore {
	return &agentStore{state: fixtureControlPlaneState(), mappings: map[string]journal.PrincipalAgent{}}
}

func fixturePrincipalIntent() PrincipalIntent {
	return PrincipalIntent{Fingerprint: "prn_aaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "hermes-private", DisplayLabel: "陈科良"}
}

func fixturePrincipalAgent(intent PrincipalIntent) journal.PrincipalAgent {
	return journal.PrincipalAgent{
		Fingerprint: intent.Fingerprint, RouteKind: intent.RouteKind, BackendUserID: "usr-owner",
		BackendTeamID: "team-owner", BackendAgentID: "agt-existing",
		BackendAssetID: "chat_memory-team-owner-agt-existing", DisplayLabel: intent.DisplayLabel, State: "active",
	}
}
