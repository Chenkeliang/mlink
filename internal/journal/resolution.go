package journal

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Resolution string

const (
	ResolutionDiscarded Resolution = "discarded"
	ResolutionDelivered Resolution = "delivered"
)

type ResolutionRequest struct {
	Resolution                      Resolution
	Reason, ResolvedBy, ProviderRef string
}

func (s *Store) ListUnresolvedEvents(ctx context.Context) ([]Event, error) {
	return s.listEvents(ctx, selectEvent+" WHERE state IN (?, ?) AND resolution IS NULL ORDER BY created_at, id", StatePermanentFailed, StateAmbiguous)
}

func (s *Store) listEvents(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Event
	for rows.Next() {
		event, e := scanEvent(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) ResolveUnresolvedEvent(ctx context.Context, ref string, req ResolutionRequest) (Event, error) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 8 || strings.TrimSpace(req.Reason) == "" || strings.TrimSpace(req.ResolvedBy) == "" ||
		(req.Resolution != ResolutionDiscarded && req.Resolution != ResolutionDelivered) ||
		(req.Resolution == ResolutionDelivered) != (strings.TrimSpace(req.ProviderRef) != "") {
		return Event{}, errors.New("valid audited event resolution is required")
	}
	events, err := s.ListUnresolvedEvents(ctx)
	if err != nil {
		return Event{}, err
	}
	var match []Event
	for _, event := range events {
		if event.ID == ref || strings.HasSuffix(event.ID, ref) {
			match = append(match, event)
		}
	}
	if len(match) == 0 {
		var resolution string
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(resolution,'') FROM journal_events WHERE id=?`, ref).Scan(&resolution); err == nil && resolution != "" {
			return Event{}, ErrInvalidTransition
		}
	}
	if len(match) != 1 {
		return Event{}, errors.New("event reference is missing or ambiguous")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE journal_events SET resolution=?,resolved_reason=?,resolved_at=?,resolved_by=?,provider_ref=?,payload=NULL,updated_at=? WHERE id=? AND resolution IS NULL AND state IN (?,?)`, req.Resolution, req.Reason, formatTime(now), req.ResolvedBy, nullString(req.ProviderRef), formatTime(now), match[0].ID, StateAmbiguous, StatePermanentFailed)
	if err != nil {
		return Event{}, err
	}
	if err := requireChanged(result); err != nil {
		return Event{}, err
	}
	return scanEvent(s.db.QueryRowContext(ctx, selectEvent+" WHERE id=?", match[0].ID))
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
