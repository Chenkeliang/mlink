package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"mlink/internal/journal"
)

type blockingEvents struct {
	events []journal.Event
}

func TestFullUninstallUsesLatestSemanticBackupAndDeletesConnectionSecret(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	installRequest := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	hermesPath := "/home/test/.hermes/config.yaml"
	file := target.files[hermesPath]
	file.content = append(file.content, []byte("later_user_setting: keep\n")...)
	target.files[hermesPath] = file

	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}
	uninstallPlan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), uninstallPlan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Binary]; exists {
		t.Fatal("MLink binary still exists")
	}
	if _, exists := secrets.values["connection/local/token"]; exists {
		t.Fatal("MemoryCore token still exists")
	}
	if _, exists := secrets.values["identity/hmac-key"]; exists {
		t.Fatal("identity key still exists")
	}
	if _, exists := secrets.values["adapter/hermes/token"]; exists {
		t.Fatal("Hermes grant still exists")
	}
	if got := target.files[hermesPath].content; !bytes.Contains(got, []byte("provider: hy-memory")) || !bytes.Contains(got, []byte("later_user_setting: keep")) {
		t.Fatalf("Hermes config after uninstall = %s", got)
	}
}

func (store blockingEvents) ListStateDeletionBlockers(context.Context) ([]journal.Event, error) {
	return append([]journal.Event(nil), store.events...), nil
}

func TestUninstallBlocksStateDeletionWithAmbiguousEvents(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	service.BlockingEvents = blockingEvents{events: []journal.Event{{ID: "event-1", State: journal.StateAmbiguous}}}
	_, err := service.PlanUninstall(context.Background(), UninstallRequest{Agents: []Agent{Codex}, RemoveState: true})
	if !errors.Is(err, journal.ErrBlockingEvents) {
		t.Fatalf("error = %v", err)
	}
}
