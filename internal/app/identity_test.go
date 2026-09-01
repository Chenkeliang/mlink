package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"mlink/internal/config"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type identityControlStore struct {
	state    journal.ControlPlaneState
	mappings map[string]journal.PrincipalAgent
}

func (store *identityControlStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	return store.state, nil
}
func (store *identityControlStore) MarkControlPlaneState(_ context.Context, state string) error {
	store.state.State = state
	return nil
}
func (store *identityControlStore) GetPrincipalAgent(_ context.Context, fingerprint string) (journal.PrincipalAgent, error) {
	return store.mappings[fingerprint], nil
}
func (store *identityControlStore) PutPrincipalAgent(_ context.Context, mapping journal.PrincipalAgent) error {
	store.mappings[mapping.Fingerprint] = mapping
	return nil
}
func (store *identityControlStore) ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error) {
	result := make([]journal.PrincipalAgent, 0, len(store.mappings))
	for _, mapping := range store.mappings {
		result = append(result, mapping)
	}
	return result, nil
}

func TestIdentityBindPreviewDoesNotWriteOrExposeValue(t *testing.T) {
	service, _, secrets := installedIdentityFixture(t)
	request := IdentityBindRequest{SlotID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", PrincipalID: "owner", Value: []byte("on_new")}
	beforePuts := secrets.puts
	plan, err := service.PlanIdentityBind(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := install.RenderJSON(plan)
	if secrets.puts != beforePuts || bytes.Contains(raw, []byte("on_new")) {
		t.Fatalf("preview wrote/leaked: puts=%d json=%s", secrets.puts-beforePuts, raw)
	}
	if err := service.ApplyIdentityBind(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if got := string(secrets.values["identity/binding/owner-feishu-union-2"]); got != "on_new" {
		t.Fatalf("binding = %q", got)
	}
}

func TestIdentityImportRefusesCanonicalUserCollision(t *testing.T) {
	service, _, _ := installedIdentityFixture(t)
	bundle := identity.BundleV1{
		SchemaVersion: 1,
		Principals:    map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_other", Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
		},
		IdentityKey: bytes.Repeat([]byte{0x2a}, 32), CreatedAt: time.Now().UTC(),
	}
	if _, err := service.PlanIdentityImport(context.Background(), bundle); !errors.Is(err, ErrIdentityImportConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestIdentityExportRoundTripPreservesOwnerAndBinding(t *testing.T) {
	service, _, secrets := installedIdentityFixture(t)
	state := journal.ControlPlaneState{InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated", OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated", State: "active"}
	store := &identityControlStore{state: state, mappings: map[string]journal.PrincipalAgent{}}
	service.ControlPlaneStates, service.PrincipalAgentStates = store, store
	secrets.values["control/tencentdb/admin-user-key"] = []byte("admin-key")
	secrets.values["control/tencentdb/owner-user-key"] = []byte("owner-key")
	encrypted, err := service.ExportIdentity(context.Background(), []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := identity.DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Wipe()
	if bundle.SchemaVersion != 2 || bundle.Principals["owner"].CanonicalUserID != "usr-owner-generated" || len(bundle.Bindings) != 1 || string(bundle.Bindings[0].Value) != "on_owner" {
		t.Fatal("exported identity does not match installed owner")
	}
}

func TestIdentityExportV2IncludesGeneratedControlPlaneAndMappings(t *testing.T) {
	service, _, secrets := installedIdentityFixture(t)
	secrets.values["control/tencentdb/admin-user-key"] = []byte("admin-key")
	secrets.values["control/tencentdb/owner-user-key"] = []byte("owner-key")
	state := journal.ControlPlaneState{
		InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated",
		OwnerAgentID: "agt-owner-generated", OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated",
		PanelContainer: "tdai-memory-hub", PanelImage: "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104", State: "provisioned",
	}
	store := &identityControlStore{state: state, mappings: map[string]journal.PrincipalAgent{
		"prn_aaaaaaaaaaaaaaaaaaaaaaaaaa": {
			Fingerprint: "prn_aaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "hermes-private", BackendUserID: state.OwnerUserID,
			BackendTeamID: state.OwnerTeamID, BackendAgentID: "agt-private", BackendAssetID: "chat_memory-team-owner-generated-agt-private",
			DisplayLabel: "Feishu DM", State: "active",
		},
	}}
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	encrypted, err := service.ExportIdentity(context.Background(), []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := identity.DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Wipe()
	if bundle.SchemaVersion != 2 || bundle.ControlPlane.OwnerAgentID != state.OwnerAgentID || len(bundle.PrincipalAgents) != 1 || len(bundle.Spaces) != 0 {
		t.Fatalf("bundle = %#v", bundle)
	}
	importPlan, err := service.PlanIdentityImport(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyIdentityImport(context.Background(), importPlan.PlanID, bundle); err != nil {
		t.Fatal(err)
	}
	if got := store.mappings["prn_aaaaaaaaaaaaaaaaaaaaaaaaaa"].State; got != "provisioning" {
		t.Fatalf("imported mapping state = %q, want reconciliation", got)
	}
	if string(secrets.values["control/tencentdb/owner-user-key"]) != "owner-key" {
		t.Fatal("Owner key was not restored from encrypted bundle")
	}
}

func TestIdentityRebindAddsNewAndRevokesOldAtomically(t *testing.T) {
	service, _, secrets := installedIdentityFixture(t)
	request := IdentityRebindRequest{
		OldSlotID:  "owner-feishu-union-1",
		NewBinding: IdentityBindRequest{SlotID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", PrincipalID: "owner", Value: []byte("on_new")},
	}
	plan, err := service.PlanIdentityRebind(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyIdentityRebind(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	descriptors, err := service.IdentityList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 || descriptors[0].SlotID != "owner-feishu-union-1" || descriptors[0].Status != "revoked" || descriptors[1].Status != "active" {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	if string(secrets.values["identity/binding/owner-feishu-union-1"]) != "on_owner" {
		t.Fatal("rebind deleted revoked value")
	}
}

func installedIdentityFixture(t *testing.T) (*Service, *memoryTarget, *memorySecrets) {
	t.Helper()
	service, target, secrets := newInstallFixture(t)
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	return service, target, secrets
}
