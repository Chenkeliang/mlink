package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"mlink/internal/app"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type maintenanceApplication struct {
	*fakeApplication
	events             []app.JournalEventDescriptor
	plan               install.ChangeSet
	applyCalls         int
	appliedPlan        string
	request            app.JournalResolutionRequest
	rotationPlan       install.ChangeSet
	rotationApplyCalls int
	upgradePlan        install.ChangeSet
	upgradeApplyCalls  int
}

func (application *maintenanceApplication) PlanUpgrade(_ context.Context, _ app.UpgradeRequest) (install.ChangeSet, error) {
	return application.upgradePlan, nil
}

func (application *maintenanceApplication) ApplyUpgrade(_ context.Context, planID string, _ app.UpgradeRequest) error {
	application.upgradeApplyCalls++
	application.appliedPlan = planID
	return nil
}

func (application *maintenanceApplication) ListUnresolvedJournalEvents(context.Context) ([]app.JournalEventDescriptor, error) {
	return application.events, nil
}

func (application *maintenanceApplication) PlanJournalResolution(_ context.Context, request app.JournalResolutionRequest) (install.ChangeSet, error) {
	application.request = request
	return application.plan, nil
}

func (application *maintenanceApplication) ApplyJournalResolution(_ context.Context, planID string, request app.JournalResolutionRequest) error {
	application.applyCalls++
	application.appliedPlan = planID
	application.request = request
	return nil
}

func (application *maintenanceApplication) PlanHermesGrantRotation(context.Context) (install.ChangeSet, error) {
	return application.rotationPlan, nil
}

func (application *maintenanceApplication) ApplyHermesGrantRotation(_ context.Context, planID string) error {
	application.rotationApplyCalls++
	application.appliedPlan = planID
	return nil
}

func TestMaintenanceJournalListJSONIsRedacted(t *testing.T) {
	application := &maintenanceApplication{fakeApplication: &fakeApplication{}, events: []app.JournalEventDescriptor{{EventSuffix: "23456789", AdapterID: "codex", State: journal.StateAmbiguous, MessageCount: 2}}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"maintenance", "journal", "list", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 0 || !strings.Contains(stdout.String(), `"event_suffix": "23456789"`) || strings.Contains(stdout.String(), "private payload") {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}

func TestMaintenanceJournalDiscardRequiresExactPlanAndYes(t *testing.T) {
	plan := install.ChangeSet{PlanID: "plan_exact", MLinkVersion: "dev", Operations: []install.Operation{{Target: "journal:event:23456789", Action: install.ActionService}}}
	application := &maintenanceApplication{fakeApplication: &fakeApplication{}, plan: plan}
	stdout := &bytes.Buffer{}
	args := []string{"maintenance", "journal", "discard", "23456789", "--reason", "legacy scope inactive", "--apply-plan", "plan_exact", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: stdout, Stderr: io.Discard})
	if code != 0 || application.applyCalls != 1 || application.appliedPlan != "plan_exact" || application.request.Resolution != journal.ResolutionDiscarded {
		t.Fatalf("code/app = %d/%#v", code, application)
	}
}

func TestMaintenanceJournalAcknowledgeRequiresProviderReference(t *testing.T) {
	application := &maintenanceApplication{fakeApplication: &fakeApplication{}, plan: install.ChangeSet{PlanID: "plan_exact"}}
	code := Run(context.Background(), []string{"maintenance", "journal", "acknowledge", "23456789", "--reason", "verified"}, Dependencies{App: application, Stdin: strings.NewReader("n\n"), Stdout: io.Discard, Stderr: io.Discard})
	if code != 2 || application.applyCalls != 0 {
		t.Fatalf("code/calls = %d/%d", code, application.applyCalls)
	}
}

func TestMaintenanceHermesGrantRotationRequiresExactPlan(t *testing.T) {
	application := &maintenanceApplication{fakeApplication: &fakeApplication{}, rotationPlan: install.ChangeSet{
		PlanID: "plan_rotate", MLinkVersion: "dev", Operations: []install.Operation{{Target: "credential:adapter/hermes/token", Action: install.ActionService}},
	}}
	stdout := &bytes.Buffer{}
	args := []string{"maintenance", "credentials", "rotate", "hermes-grant", "--apply-plan", "plan_rotate", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: stdout, Stderr: io.Discard})
	if code != 0 || application.rotationApplyCalls != 1 || application.appliedPlan != "plan_rotate" || strings.Contains(strings.ToLower(stdout.String()), "secret") {
		t.Fatalf("code/application/output = %d/%#v/%s", code, application, stdout)
	}
}

func TestMaintenanceUpgradeRequiresAbsoluteCandidateAndExactPlan(t *testing.T) {
	application := &maintenanceApplication{fakeApplication: &fakeApplication{}, upgradePlan: install.ChangeSet{
		PlanID: "plan_upgrade", MLinkVersion: "1.0.0", Operations: []install.Operation{{Target: "/Users/test/.local/bin/mlink", Action: install.ActionSemanticMerge}},
	}}
	args := []string{"maintenance", "upgrade", "--candidate", "/tmp/mlink-new", "--apply-plan", "plan_upgrade", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
	if code != 0 || application.upgradeApplyCalls != 1 || application.appliedPlan != "plan_upgrade" {
		t.Fatalf("code/application = %d/%#v", code, application)
	}
	if code := Run(context.Background(), []string{"maintenance", "upgrade", "--candidate", "relative"}, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
		t.Fatalf("relative candidate code = %d", code)
	}
}
