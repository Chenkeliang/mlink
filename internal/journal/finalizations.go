package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
)

const finalizationDispatchLease = 30 * time.Second

type FinalizationRequest struct {
	AdapterID   string
	Route       connection.RouteKey
	Identity    model.IdentityScope
	ActorDigest string
}

type Finalization struct {
	ID             string
	IdempotencyKey string
	AdapterID      string
	Route          connection.RouteKey
	Identity       model.IdentityScope
	ActorDigest    string
	State          State
	AttemptCount   int
	NextAttemptAt  *time.Time
	ErrorCode      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) RequestFinalization(ctx context.Context, request FinalizationRequest) (Finalization, bool, int, error) {
	if strings.TrimSpace(request.AdapterID) == "" ||
		request.Route.ConnectionID == "" || request.Route.ProviderID == "" ||
		request.Route.ProviderVersion == "" || request.Route.ConfigRevision == "" {
		return Finalization{}, false, 0, errors.New("finalization adapter and complete route are required")
	}
	if err := request.Identity.ValidateForRecall(); err != nil {
		return Finalization{}, false, 0, err
	}
	if request.Identity.ConnectionID != request.Route.ConnectionID || strings.TrimSpace(request.Identity.SessionID) == "" {
		return Finalization{}, false, 0, errors.New("finalization identity does not match route or lacks session_id")
	}
	identity := request.Identity
	idempotencyKey := hashParts(
		request.AdapterID, request.Route.ConnectionID, identity.TenantID,
		identity.AgentID, identity.UserID, identity.SessionID,
	)
	now := time.Now().UTC()
	finalization := Finalization{
		ID: "fin_" + idempotencyKey[:26], IdempotencyKey: idempotencyKey,
		AdapterID: request.AdapterID, Route: request.Route, Identity: identity,
		ActorDigest: request.ActorDigest, State: StateQueued, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Finalization{}, false, 0, fmt.Errorf("begin finalization request: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM turn_fragments
		WHERE rowid IN (
			SELECT fragment.rowid
			FROM turn_fragments AS fragment
			WHERE fragment.adapter_id = ? AND fragment.connection_id = ?
			  AND fragment.tenant_id = ? AND fragment.agent_id = ?
			  AND fragment.user_id = ? AND fragment.session_id = ?
			  AND NOT (
				EXISTS (
					SELECT 1 FROM turn_fragments AS user_fragment
					WHERE user_fragment.adapter_id = fragment.adapter_id
					  AND user_fragment.connection_id = fragment.connection_id
					  AND user_fragment.tenant_id = fragment.tenant_id
					  AND user_fragment.agent_id = fragment.agent_id
					  AND user_fragment.user_id = fragment.user_id
					  AND user_fragment.session_id = fragment.session_id
					  AND user_fragment.turn_id = fragment.turn_id
					  AND user_fragment.role = 'user'
				)
				AND EXISTS (
					SELECT 1 FROM turn_fragments AS assistant_fragment
					WHERE assistant_fragment.adapter_id = fragment.adapter_id
					  AND assistant_fragment.connection_id = fragment.connection_id
					  AND assistant_fragment.tenant_id = fragment.tenant_id
					  AND assistant_fragment.agent_id = fragment.agent_id
					  AND assistant_fragment.user_id = fragment.user_id
					  AND assistant_fragment.session_id = fragment.session_id
					  AND assistant_fragment.turn_id = fragment.turn_id
					  AND assistant_fragment.role = 'assistant'
				)
			)
		)`,
		request.AdapterID, request.Route.ConnectionID, identity.TenantID,
		identity.AgentID, identity.UserID, identity.SessionID,
	); err != nil {
		return Finalization{}, false, 0, fmt.Errorf("clear incomplete finalization fragments: %w", err)
	}

	var pending int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM journal_events
		WHERE adapter_id = ? AND connection_id = ? AND tenant_id = ? AND agent_id = ?
		  AND user_id = ? AND session_id = ? AND state IN (?, ?, ?)`,
		request.AdapterID, request.Route.ConnectionID, identity.TenantID, identity.AgentID,
		identity.UserID, identity.SessionID, StateQueued, StateDispatching, StateRetryableFailed,
	).Scan(&pending); err != nil {
		return Finalization{}, false, 0, fmt.Errorf("count pending finalization turns: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO session_finalizations(
			id, idempotency_key, adapter_id, connection_id, provider_id, provider_version,
			config_revision, tenant_id, agent_id, user_id, session_id, actor_digest,
			state, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id)
		DO UPDATE SET
			provider_id = excluded.provider_id,
			provider_version = excluded.provider_version,
			config_revision = excluded.config_revision,
			actor_digest = excluded.actor_digest,
			state = excluded.state,
			attempt_count = 0,
			next_attempt_at = NULL,
			error_code = NULL,
			updated_at = excluded.updated_at
		WHERE session_finalizations.state = ?`,
		finalization.ID, finalization.IdempotencyKey, request.AdapterID,
		request.Route.ConnectionID, request.Route.ProviderID, request.Route.ProviderVersion,
		request.Route.ConfigRevision, identity.TenantID, identity.AgentID, identity.UserID,
		identity.SessionID, request.ActorDigest, StateQueued, formatTime(now), formatTime(now),
		StateCompleted,
	)
	if err != nil {
		return Finalization{}, false, 0, fmt.Errorf("persist finalization request: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Finalization{}, false, 0, fmt.Errorf("inspect finalization request: %w", err)
	}
	loaded, err := scanFinalization(tx.QueryRowContext(ctx, selectFinalization+`
		WHERE adapter_id = ? AND connection_id = ? AND tenant_id = ? AND agent_id = ?
		  AND user_id = ? AND session_id = ?`,
		request.AdapterID, request.Route.ConnectionID, identity.TenantID, identity.AgentID,
		identity.UserID, identity.SessionID,
	))
	if err != nil {
		return Finalization{}, false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return Finalization{}, false, 0, fmt.Errorf("commit finalization request: %w", err)
	}
	return loaded, changed == 1, pending, nil
}

func (s *Store) Finalization(ctx context.Context, id string) (Finalization, error) {
	return scanFinalization(s.db.QueryRowContext(ctx, selectFinalization+" WHERE id = ?", id))
}

func (s *Store) ClaimReadyFinalizations(ctx context.Context, now time.Time, limit int) ([]Finalization, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin finalization claim: %w", err)
	}
	defer tx.Rollback()
	formattedNow := formatTime(now)
	rows, err := tx.QueryContext(ctx, selectFinalization+`
		WHERE (
			(state IN (?, ?) AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
			OR (state = ? AND next_attempt_at <= ?)
		)
		AND NOT EXISTS (
			SELECT 1 FROM journal_events AS event
			WHERE event.adapter_id = session_finalizations.adapter_id
			  AND event.connection_id = session_finalizations.connection_id
			  AND event.tenant_id = session_finalizations.tenant_id
			  AND event.agent_id = session_finalizations.agent_id
			  AND event.user_id = session_finalizations.user_id
			  AND event.session_id = session_finalizations.session_id
			  AND (
				event.state IN (?, ?, ?)
				OR (event.state IN (?, ?) AND event.resolution IS NULL)
			  )
		)
		AND NOT EXISTS (
			SELECT 1 FROM turn_fragments AS fragment
			WHERE fragment.adapter_id = session_finalizations.adapter_id
			  AND fragment.connection_id = session_finalizations.connection_id
			  AND fragment.tenant_id = session_finalizations.tenant_id
			  AND fragment.agent_id = session_finalizations.agent_id
			  AND fragment.user_id = session_finalizations.user_id
			  AND fragment.session_id = session_finalizations.session_id
		)
		ORDER BY created_at, id
		LIMIT ?`,
		StateQueued, StateRetryableFailed, formattedNow, StateDispatching, formattedNow,
		StateQueued, StateDispatching, StateRetryableFailed, StatePermanentFailed, StateAmbiguous,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("select ready finalizations: %w", err)
	}
	var finalizations []Finalization
	for rows.Next() {
		finalization, err := scanFinalization(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		finalizations = append(finalizations, finalization)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close ready finalizations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ready finalizations: %w", err)
	}
	leaseUntil := now.Add(finalizationDispatchLease)
	for i := range finalizations {
		result, err := tx.ExecContext(ctx, `
			UPDATE session_finalizations
			SET state = ?, attempt_count = attempt_count + 1, next_attempt_at = ?, updated_at = ?
			WHERE id = ? AND (
				(state IN (?, ?) AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
				OR (state = ? AND next_attempt_at <= ?)
			)`,
			StateDispatching, formatTime(leaseUntil), formattedNow, finalizations[i].ID,
			StateQueued, StateRetryableFailed, formattedNow, StateDispatching, formattedNow,
		)
		if err != nil {
			return nil, fmt.Errorf("claim finalization %q: %w", finalizations[i].ID, err)
		}
		if err := requireChanged(result); err != nil {
			return nil, err
		}
		finalizations[i].State = StateDispatching
		finalizations[i].AttemptCount++
		finalizations[i].NextAttemptAt = &leaseUntil
		finalizations[i].UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit finalization claim: %w", err)
	}
	return finalizations, nil
}

func (s *Store) MarkFinalizationCompleted(ctx context.Context, id string) error {
	return s.markFinalization(ctx, id, StateCompleted, "", nil)
}

func (s *Store) MarkFinalizationRetryable(ctx context.Context, id, errorCode string, nextAttemptAt time.Time) error {
	return s.markFinalization(ctx, id, StateRetryableFailed, errorCode, &nextAttemptAt)
}

func (s *Store) MarkFinalizationPermanent(ctx context.Context, id, errorCode string) error {
	return s.markFinalization(ctx, id, StatePermanentFailed, errorCode, nil)
}

func (s *Store) MarkFinalizationAmbiguous(ctx context.Context, id, errorCode string) error {
	return s.markFinalization(ctx, id, StateAmbiguous, errorCode, nil)
}

func (s *Store) markFinalization(ctx context.Context, id string, state State, errorCode string, next *time.Time) error {
	var nextValue any
	if next != nil {
		nextValue = formatTime(*next)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_finalizations
		SET state = ?, next_attempt_at = ?, error_code = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		state, nextValue, nullString(errorCode), formatTime(time.Now().UTC()), id, StateDispatching,
	)
	if err != nil {
		return fmt.Errorf("mark finalization %s: %w", state, err)
	}
	return requireChanged(result)
}

const selectFinalization = `SELECT
	id, idempotency_key, adapter_id, connection_id, provider_id, provider_version,
	config_revision, tenant_id, agent_id, user_id, session_id, actor_digest,
	state, attempt_count, next_attempt_at, error_code, created_at, updated_at
	FROM session_finalizations`

func scanFinalization(row rowScanner) (Finalization, error) {
	var finalization Finalization
	var nextAttempt, errorCode sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&finalization.ID, &finalization.IdempotencyKey, &finalization.AdapterID,
		&finalization.Route.ConnectionID, &finalization.Route.ProviderID,
		&finalization.Route.ProviderVersion, &finalization.Route.ConfigRevision,
		&finalization.Identity.TenantID, &finalization.Identity.AgentID,
		&finalization.Identity.UserID, &finalization.Identity.SessionID,
		&finalization.ActorDigest, &finalization.State, &finalization.AttemptCount,
		&nextAttempt, &errorCode, &createdAt, &updatedAt,
	); err != nil {
		return Finalization{}, err
	}
	finalization.Identity.ConnectionID = finalization.Route.ConnectionID
	created, err := parseTime(createdAt)
	if err != nil {
		return Finalization{}, err
	}
	updated, err := parseTime(updatedAt)
	if err != nil {
		return Finalization{}, err
	}
	finalization.CreatedAt = created
	finalization.UpdatedAt = updated
	if nextAttempt.Valid {
		parsed, err := parseTime(nextAttempt.String)
		if err != nil {
			return Finalization{}, err
		}
		finalization.NextAttemptAt = &parsed
	}
	if errorCode.Valid {
		finalization.ErrorCode = errorCode.String
	}
	return finalization, nil
}
