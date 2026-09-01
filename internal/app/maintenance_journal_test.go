package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/model"
)

type journalMaintenanceStore struct {
	events       []journal.Event
	resolved     journal.ResolutionRequest
	resolvedRef  string
	resolveCalls int
}

func (store *journalMaintenanceStore) ListUnresolvedEvents(context.Context) ([]journal.Event, error) {
	return append([]journal.Event(nil), store.events...), nil
}

func (store *journalMaintenanceStore) ResolveUnresolvedEvent(_ context.Context, ref string, request journal.ResolutionRequest) (journal.Event, error) {
	store.resolveCalls++
	store.resolvedRef = ref
	store.resolved = request
	for index, event := range store.events {
		if event.ID == ref {
			store.events[index].Resolution = request.Resolution
			return store.events[index], nil
		}
	}
	return journal.Event{}, errors.New("not found")
}

func fixtureUnresolvedEvent() journal.Event {
	return journal.Event{
		ID: "evt_0123456789abcdef0123456789", AdapterID: "codex", State: journal.StateAmbiguous,
		Route:       connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3"},
		Turn:        model.Turn{Identity: model.IdentityScope{TenantID: "team-secret", AgentID: "agent-secret", UserID: "user-secret", SessionID: "session-secret", TurnID: "turn-secret"}, Messages: []model.Message{{Role: "user", Content: "private payload"}, {Role: "assistant", Content: "private response"}}},
		ContentHash: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", AttemptCount: 1, ErrorCode: "permanent_failure",
		CreatedAt: time.Date(2026, 9, 1, 2, 57, 29, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 2, 58, 0, 0, time.UTC),
	}
}

func TestJournalUnresolvedDescriptorsAreRedacted(t *testing.T) {
	store := &journalMaintenanceStore{events: []journal.Event{fixtureUnresolvedEvent()}}
	service := Service{JournalMaintenance: store}
	descriptors, err := service.ListUnresolvedJournalEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].EventSuffix != "23456789" || descriptors[0].MessageCount != 2 {
		t.Fatalf("descriptors = %#v", descriptors)
	}
	serialized := descriptors[0].String()
	for _, secret := range []string{"private payload", "private response", "team-secret", "agent-secret", "user-secret", "session-secret", "turn-secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("descriptor leaked %q: %s", secret, serialized)
		}
	}
}

func TestPlanJournalResolutionRequiresAuditEvidence(t *testing.T) {
	service := Service{Target: install.LocalTarget{}, JournalMaintenance: &journalMaintenanceStore{events: []journal.Event{fixtureUnresolvedEvent()}}}
	for name, request := range map[string]JournalResolutionRequest{
		"reason":       {EventRef: "23456789", Resolution: journal.ResolutionDiscarded},
		"provider ref": {EventRef: "23456789", Resolution: journal.ResolutionDelivered, Reason: "verified"},
		"short suffix": {EventRef: "56789", Resolution: journal.ResolutionDiscarded, Reason: "legacy"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.PlanJournalResolution(context.Background(), request); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestApplyJournalResolutionRevalidatesExactPlan(t *testing.T) {
	store := &journalMaintenanceStore{events: []journal.Event{fixtureUnresolvedEvent()}}
	service := Service{Target: install.LocalTarget{}, JournalMaintenance: store, OperatorID: "uid:501@host"}
	request := JournalResolutionRequest{EventRef: "23456789", Resolution: journal.ResolutionDiscarded, Reason: "legacy scope inactive; backend L0 session empty"}
	plan, err := service.PlanJournalResolution(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Operations[0].ProposedHash, request.Reason) || strings.Contains(plan.Operations[0].Target, fixtureUnresolvedEvent().Turn.Messages[0].Content) {
		t.Fatal("plan leaked sensitive content")
	}
	store.events[0].ContentHash = strings.Repeat("0", 64)
	if err := service.ApplyJournalResolution(context.Background(), plan.PlanID, request); !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("ApplyJournalResolution() error = %v, want stale", err)
	}
	if store.resolveCalls != 0 {
		t.Fatalf("resolve calls = %d", store.resolveCalls)
	}
	store.events[0] = fixtureUnresolvedEvent()
	plan, err = service.PlanJournalResolution(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyJournalResolution(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if store.resolveCalls != 1 || store.resolvedRef != fixtureUnresolvedEvent().ID || store.resolved.ResolvedBy != "uid:501@host" {
		t.Fatalf("resolution = calls:%d ref:%q request:%#v", store.resolveCalls, store.resolvedRef, store.resolved)
	}
}
