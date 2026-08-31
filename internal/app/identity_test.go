package app

import (
	"bytes"
	"context"
	"testing"

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
