package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mlink/internal/install"
	"mlink/internal/journal"
)

type JournalEventDescriptor struct {
	EventSuffix      string        `json:"event_suffix"`
	AdapterID        string        `json:"adapter_id"`
	State            journal.State `json:"state"`
	AttemptCount     int           `json:"attempt_count"`
	ErrorCode        string        `json:"error_code,omitempty"`
	RouteFingerprint string        `json:"route_fingerprint"`
	ContentHash      string        `json:"content_hash_suffix"`
	MessageCount     int           `json:"message_count"`
	PayloadBytes     int           `json:"payload_bytes"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (descriptor JournalEventDescriptor) String() string {
	encoded, _ := json.Marshal(descriptor)
	return string(encoded)
}

type JournalResolutionRequest struct {
	EventRef    string
	Resolution  journal.Resolution
	Reason      string
	ProviderRef string
}

func (service *Service) ListUnresolvedJournalEvents(ctx context.Context) ([]JournalEventDescriptor, error) {
	if service.JournalMaintenance == nil {
		return nil, errors.New("journal maintenance store is required")
	}
	events, err := service.JournalMaintenance.ListUnresolvedEvents(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]JournalEventDescriptor, 0, len(events))
	for _, event := range events {
		payload, _ := json.Marshal(event.Turn)
		result = append(result, JournalEventDescriptor{
			EventSuffix: eventSuffix(event.ID), AdapterID: event.AdapterID, State: event.State,
			AttemptCount: event.AttemptCount, ErrorCode: event.ErrorCode,
			RouteFingerprint: shortDigest(event.Route.ConnectionID, event.Route.ProviderID, event.Route.ProviderVersion, event.Route.ConfigRevision),
			ContentHash:      suffix(event.ContentHash, 12), MessageCount: len(event.Turn.Messages), PayloadBytes: len(payload),
			CreatedAt: event.CreatedAt, UpdatedAt: event.UpdatedAt,
		})
	}
	return result, nil
}

func (service *Service) PlanJournalResolution(ctx context.Context, request JournalResolutionRequest) (install.ChangeSet, error) {
	event, err := service.validateJournalResolution(ctx, request)
	if err != nil {
		return install.ChangeSet{}, err
	}
	intent, err := json.Marshal(struct {
		EventID, State, ContentHash, Resolution, Reason, ProviderRef, ResolvedBy string
	}{event.ID, string(event.State), event.ContentHash, string(request.Resolution), strings.TrimSpace(request.Reason), strings.TrimSpace(request.ProviderRef), service.OperatorID})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.journal", Target: "journal:event:" + eventSuffix(event.ID), Action: install.ActionService,
		Command: []string{"mlink-internal", "journal-resolve"}, CommandInput: intent,
		SemanticDiff: []install.SemanticDiff{
			{Path: "delivery.state", Before: string(event.State), After: string(event.State)},
			{Path: "resolution", Before: "unresolved", After: string(request.Resolution)},
			{Path: "audit.reason", Before: "absent", After: "provided"},
			{Path: "payload", Before: "encrypted/content-bearing", After: "purged"},
		},
	}})
}

func (service *Service) ApplyJournalResolution(ctx context.Context, planID string, request JournalResolutionRequest) error {
	if strings.TrimSpace(service.OperatorID) == "" {
		return errors.New("journal maintenance operator identity is required")
	}
	plan, err := service.PlanJournalResolution(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: journal event changed after preview", install.ErrPlanStale)
	}
	event, err := service.findUnresolvedEvent(ctx, request.EventRef)
	if err != nil {
		return err
	}
	_, err = service.JournalMaintenance.ResolveUnresolvedEvent(ctx, event.ID, journal.ResolutionRequest{
		Resolution: request.Resolution, Reason: strings.TrimSpace(request.Reason), ResolvedBy: service.OperatorID, ProviderRef: strings.TrimSpace(request.ProviderRef),
	})
	return err
}

func (service *Service) validateJournalResolution(ctx context.Context, request JournalResolutionRequest) (journal.Event, error) {
	if service.JournalMaintenance == nil || service.Target == nil {
		return journal.Event{}, errors.New("journal maintenance store and target are required")
	}
	request.EventRef = strings.TrimSpace(request.EventRef)
	request.Reason = strings.TrimSpace(request.Reason)
	request.ProviderRef = strings.TrimSpace(request.ProviderRef)
	if len(request.EventRef) < 8 || request.Reason == "" || (request.Resolution != journal.ResolutionDiscarded && request.Resolution != journal.ResolutionDelivered) ||
		(request.Resolution == journal.ResolutionDelivered) != (request.ProviderRef != "") {
		return journal.Event{}, errors.New("valid audited journal resolution is required")
	}
	return service.findUnresolvedEvent(ctx, request.EventRef)
}

func (service *Service) findUnresolvedEvent(ctx context.Context, ref string) (journal.Event, error) {
	events, err := service.JournalMaintenance.ListUnresolvedEvents(ctx)
	if err != nil {
		return journal.Event{}, err
	}
	var matches []journal.Event
	for _, event := range events {
		if event.ID == ref || strings.HasSuffix(event.ID, ref) {
			matches = append(matches, event)
		}
	}
	if len(matches) != 1 {
		return journal.Event{}, errors.New("journal event reference is missing or ambiguous")
	}
	return matches[0], nil
}

func eventSuffix(value string) string { return suffix(value, 8) }

func suffix(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[len(value)-length:]
}

func shortDigest(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])[:12]
}
