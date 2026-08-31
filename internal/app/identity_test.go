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
)

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
	service, _, _ := installedIdentityFixture(t)
	encrypted, err := service.ExportIdentity(context.Background(), []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := identity.DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Wipe()
	if bundle.Principals["owner"].CanonicalUserID != "usr_owner_keliang" || len(bundle.Bindings) != 1 || string(bundle.Bindings[0].Value) != "on_owner" {
		t.Fatal("exported identity does not match installed owner")
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
