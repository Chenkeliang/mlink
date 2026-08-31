package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
)

var (
	ErrTurnConflict      = errors.New("turn conflicts with an existing event")
	ErrInvalidTransition = errors.New("invalid journal state transition")
	ErrBlockingEvents    = errors.New("journal has events that block state deletion")
)

type State string

const (
	StateQueued          State = "queued"
	StateDispatching     State = "dispatching"
	StateAccepted        State = "accepted"
	StateVisible         State = "visible"
	StateRetryableFailed State = "retryable_failed"
	StatePermanentFailed State = "permanent_failed"
	StateAmbiguous       State = "ambiguous"
)

type Envelope struct {
	AdapterID string
	Route     connection.RouteKey
	Turn      model.Turn
}

type Fragment struct {
	AdapterID   string
	Route       connection.RouteKey
	Identity    model.IdentityScope
	ActorDigest string
	Role        string
	Content     string
	OccurredAt  time.Time
}

type Event struct {
	ID             string
	IdempotencyKey string
	AdapterID      string
	Route          connection.RouteKey
	Turn           model.Turn
	ContentHash    string
	State          State
	AttemptCount   int
	NextAttemptAt  *time.Time
	Receipt        model.WriteReceipt
	ErrorCode      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) RecordFragment(ctx context.Context, fragment Fragment) error {
	if strings.TrimSpace(fragment.AdapterID) == "" || strings.TrimSpace(fragment.Role) == "" || strings.TrimSpace(fragment.Content) == "" {
		return errors.New("fragment adapter, role, and content are required")
	}
	if err := fragment.Identity.ValidateForCapture(); err != nil {
		return err
	}
	hash := hashBytes([]byte(fragment.Content))
	occurredAt := fragment.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO turn_fragments(
			adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id,
			role, content, content_hash, occurred_at, provider_id, provider_version, config_revision, actor_digest
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id, role)
		DO UPDATE SET occurred_at = excluded.occurred_at
		WHERE turn_fragments.content_hash = excluded.content_hash
		  AND turn_fragments.actor_digest = excluded.actor_digest`,
		fragment.AdapterID,
		fragment.Route.ConnectionID,
		fragment.Identity.TenantID,
		fragment.Identity.AgentID,
		fragment.Identity.UserID,
		fragment.Identity.SessionID,
		fragment.Identity.TurnID,
		fragment.Role,
		[]byte(fragment.Content),
		hash,
		formatTime(occurredAt),
		fragment.Route.ProviderID,
		fragment.Route.ProviderVersion,
		fragment.Route.ConfigRevision,
		fragment.ActorDigest,
	)
	if err != nil {
		return fmt.Errorf("record turn fragment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect turn fragment: %w", err)
	}
	if changed == 0 {
		return ErrTurnConflict
	}
	pair, complete, err := s.fragmentPair(ctx, fragment)
	if err != nil || !complete {
		return err
	}
	if _, _, err := s.EnqueueTurn(ctx, pair); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM turn_fragments
		WHERE adapter_id = ? AND connection_id = ? AND tenant_id = ? AND agent_id = ?
		  AND user_id = ? AND session_id = ? AND turn_id = ?`,
		fragment.AdapterID,
		fragment.Route.ConnectionID,
		fragment.Identity.TenantID,
		fragment.Identity.AgentID,
		fragment.Identity.UserID,
		fragment.Identity.SessionID,
		fragment.Identity.TurnID,
	)
	if err != nil {
		return fmt.Errorf("clear completed turn fragments: %w", err)
	}
	return nil
}

func (s *Store) fragmentPair(ctx context.Context, fragment Fragment) (Envelope, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT role, content, occurred_at, provider_id, provider_version, config_revision, actor_digest
		FROM turn_fragments
		WHERE adapter_id = ? AND connection_id = ? AND tenant_id = ? AND agent_id = ?
		  AND user_id = ? AND session_id = ? AND turn_id = ?`,
		fragment.AdapterID,
		fragment.Route.ConnectionID,
		fragment.Identity.TenantID,
		fragment.Identity.AgentID,
		fragment.Identity.UserID,
		fragment.Identity.SessionID,
		fragment.Identity.TurnID,
	)
	if err != nil {
		return Envelope{}, false, fmt.Errorf("read turn fragments: %w", err)
	}
	defer rows.Close()
	messages := make(map[string]model.Message, 2)
	var route connection.RouteKey
	actorDigest := ""
	for rows.Next() {
		var role, content, occurredAt, providerID, providerVersion, configRevision, currentActorDigest string
		if err := rows.Scan(&role, &content, &occurredAt, &providerID, &providerVersion, &configRevision, &currentActorDigest); err != nil {
			return Envelope{}, false, fmt.Errorf("scan turn fragment: %w", err)
		}
		parsed, err := parseTime(occurredAt)
		if err != nil {
			return Envelope{}, false, err
		}
		currentRoute := connection.RouteKey{
			ConnectionID:    fragment.Route.ConnectionID,
			ProviderID:      providerID,
			ProviderVersion: providerVersion,
			ConfigRevision:  configRevision,
		}
		if route.ConnectionID != "" && route != currentRoute {
			return Envelope{}, false, ErrTurnConflict
		}
		if actorDigest != "" && actorDigest != currentActorDigest {
			return Envelope{}, false, ErrTurnConflict
		}
		route = currentRoute
		actorDigest = currentActorDigest
		messages[role] = model.Message{Role: role, Content: content, OccurredAt: parsed}
	}
	if err := rows.Err(); err != nil {
		return Envelope{}, false, fmt.Errorf("read turn fragments: %w", err)
	}
	user, hasUser := messages["user"]
	assistant, hasAssistant := messages["assistant"]
	if !hasUser || !hasAssistant {
		return Envelope{}, false, nil
	}
	return Envelope{
		AdapterID: fragment.AdapterID,
		Route:     route,
		Turn: model.Turn{
			Identity:    fragment.Identity,
			Messages:    []model.Message{user, assistant},
			ActorDigest: actorDigest,
		},
	}, true, nil
}

func (s *Store) EnqueueTurn(ctx context.Context, envelope Envelope) (Event, bool, error) {
	if strings.TrimSpace(envelope.AdapterID) == "" {
		return Event{}, false, errors.New("adapter_id is required")
	}
	if err := envelope.Turn.Identity.ValidateForCapture(); err != nil {
		return Event{}, false, err
	}
	if len(envelope.Turn.Messages) == 0 {
		return Event{}, false, errors.New("turn messages are required")
	}
	if envelope.Route.ConnectionID == "" || envelope.Route.ProviderID == "" || envelope.Route.ProviderVersion == "" || envelope.Route.ConfigRevision == "" {
		return Event{}, false, errors.New("complete route is required")
	}
	messageJSON, err := json.Marshal(contentHashMessages(envelope.Turn.Messages))
	if err != nil {
		return Event{}, false, fmt.Errorf("encode turn messages: %w", err)
	}
	contentHash := hashBytes(messageJSON)
	identity := envelope.Turn.Identity
	idempotencyKey := hashParts(
		envelope.Route.ConnectionID,
		envelope.Route.ProviderID,
		envelope.Route.ProviderVersion,
		envelope.Route.ConfigRevision,
		identity.TenantID,
		identity.AgentID,
		identity.UserID,
		identity.SessionID,
		identity.TurnID,
		envelope.Turn.ActorDigest,
		contentHash,
	)
	payload, err := json.Marshal(envelope.Turn)
	if err != nil {
		return Event{}, false, fmt.Errorf("encode turn: %w", err)
	}
	now := time.Now().UTC()
	event := Event{
		ID:             "evt_" + idempotencyKey[:26],
		IdempotencyKey: idempotencyKey,
		AdapterID:      envelope.AdapterID,
		Route:          envelope.Route,
		Turn:           envelope.Turn,
		ContentHash:    contentHash,
		State:          StateQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, false, fmt.Errorf("begin enqueue: %w", err)
	}
	defer tx.Rollback()
	existing, err := eventByScope(ctx, tx, envelope.AdapterID, envelope.Route.ConnectionID, identity)
	if err == nil {
		if existing.IdempotencyKey == idempotencyKey && existing.ContentHash == contentHash {
			return existing, false, nil
		}
		return Event{}, false, ErrTurnConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO journal_events(
			id, idempotency_key, adapter_id, session_id, turn_id,
			connection_id, provider_id, provider_version, config_revision,
			tenant_id, agent_id, user_id, actor_digest, content_hash, payload, state,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.IdempotencyKey,
		event.AdapterID,
		identity.SessionID,
		identity.TurnID,
		event.Route.ConnectionID,
		event.Route.ProviderID,
		event.Route.ProviderVersion,
		event.Route.ConfigRevision,
		identity.TenantID,
		identity.AgentID,
		identity.UserID,
		envelope.Turn.ActorDigest,
		event.ContentHash,
		payload,
		event.State,
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return Event{}, false, fmt.Errorf("insert journal event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, false, fmt.Errorf("commit journal event: %w", err)
	}
	return event, true, nil
}

func contentHashMessages(messages []model.Message) []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
} {
	content := make([]struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}, len(messages))
	for i, message := range messages {
		content[i].Role = message.Role
		content[i].Content = message.Content
	}
	return content
}

func (s *Store) Event(ctx context.Context, id string) (Event, error) {
	return scanEvent(s.db.QueryRowContext(ctx, selectEvent+" WHERE id = ?", id))
}

func (s *Store) ClaimReady(ctx context.Context, now time.Time, limit int) ([]Event, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, selectEvent+`
		WHERE state IN (?, ?)
		  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY created_at, id
		LIMIT ?`, StateQueued, StateRetryableFailed, formatTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("select ready events: %w", err)
	}
	var events []Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close ready events: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ready events: %w", err)
	}
	for i := range events {
		result, err := tx.ExecContext(ctx, `
			UPDATE journal_events
			SET state = ?, attempt_count = attempt_count + 1, next_attempt_at = NULL, updated_at = ?
			WHERE id = ? AND state IN (?, ?)`,
			StateDispatching, formatTime(now), events[i].ID, StateQueued, StateRetryableFailed)
		if err != nil {
			return nil, fmt.Errorf("claim event %q: %w", events[i].ID, err)
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return nil, ErrInvalidTransition
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO delivery_attempts(event_id, started_at) VALUES(?, ?)", events[i].ID, formatTime(now)); err != nil {
			return nil, fmt.Errorf("record delivery attempt: %w", err)
		}
		events[i].State = StateDispatching
		events[i].AttemptCount++
		events[i].NextAttemptAt = nil
		events[i].UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return events, nil
}

func (s *Store) MarkAccepted(ctx context.Context, id string, receipt model.WriteReceipt) error {
	return s.markSuccess(ctx, id, StateAccepted, receipt)
}

func (s *Store) MarkVisible(ctx context.Context, id string, receipt model.WriteReceipt) error {
	return s.markSuccess(ctx, id, StateVisible, receipt)
}

func (s *Store) markSuccess(ctx context.Context, id string, state State, receipt model.WriteReceipt) error {
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode write receipt: %w", err)
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin successful transition: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE journal_events
		SET state = ?, payload = NULL, receipt_json = ?, error_code = NULL, updated_at = ?
		WHERE id = ? AND state IN (?, ?, ?)`,
		state, receiptJSON, formatTime(now), id, StateQueued, StateDispatching, StateAccepted)
	if err != nil {
		return fmt.Errorf("mark event %s: %w", state, err)
	}
	if err := requireChanged(result); err != nil {
		return err
	}
	if err := finishLatestAttempt(ctx, tx, id, state, "", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit successful transition: %w", err)
	}
	return nil
}

func (s *Store) MarkRetryable(ctx context.Context, id, errorCode string, nextAttemptAt time.Time) error {
	return s.markFailure(ctx, id, StateRetryableFailed, errorCode, &nextAttemptAt, []State{StateDispatching})
}

func (s *Store) MarkPermanent(ctx context.Context, id, errorCode string) error {
	return s.markFailure(ctx, id, StatePermanentFailed, errorCode, nil, []State{StateDispatching, StateQueued})
}

func (s *Store) MarkAmbiguous(ctx context.Context, id, errorCode string) error {
	return s.markFailure(ctx, id, StateAmbiguous, errorCode, nil, []State{StateDispatching, StateQueued})
}

func (s *Store) markFailure(ctx context.Context, id string, state State, errorCode string, next *time.Time, from []State) error {
	now := time.Now().UTC()
	var nextValue any
	if next != nil {
		nextValue = formatTime(*next)
	}
	placeholders := make([]string, len(from))
	args := []any{state, errorCode, nextValue, formatTime(now), id}
	for i, value := range from {
		placeholders[i] = "?"
		args = append(args, value)
	}
	query := `UPDATE journal_events
		SET state = ?, error_code = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ? AND state IN (` + strings.Join(placeholders, ",") + `)`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failed transition: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark event %s: %w", state, err)
	}
	if err := requireChanged(result); err != nil {
		return err
	}
	if err := finishLatestAttempt(ctx, tx, id, state, errorCode, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed transition: %w", err)
	}
	return nil
}

func (s *Store) FlushSession(ctx context.Context, adapterID, sessionID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM journal_events
		WHERE adapter_id = ? AND session_id = ? AND state IN (?, ?, ?)`,
		adapterID, sessionID, StateQueued, StateDispatching, StateRetryableFailed).Scan(&count)
	return count, err
}

func (s *Store) ListBlockingEvents(ctx context.Context) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, selectEvent+" WHERE state IN (?, ?) ORDER BY created_at, id", StatePermanentFailed, StateAmbiguous)
	if err != nil {
		return nil, fmt.Errorf("list blocking events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ListStateDeletionBlockers(ctx context.Context) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, selectEvent+" WHERE state IN (?, ?, ?, ?, ?) ORDER BY created_at, id",
		StateQueued, StateDispatching, StateRetryableFailed, StatePermanentFailed, StateAmbiguous)
	if err != nil {
		return nil, fmt.Errorf("list state deletion blockers: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

const selectEvent = `SELECT
	id, idempotency_key, adapter_id, session_id, turn_id,
	connection_id, provider_id, provider_version, config_revision,
	tenant_id, agent_id, user_id, actor_digest, content_hash, payload, state,
	attempt_count, next_attempt_at, receipt_json, error_code, created_at, updated_at
	FROM journal_events`

type rowScanner interface {
	Scan(...any) error
}

func scanEvent(row rowScanner) (Event, error) {
	var event Event
	var sessionID, turnID string
	var actorDigest string
	var payload, receiptJSON []byte
	var nextAttempt, errorCode sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(
		&event.ID,
		&event.IdempotencyKey,
		&event.AdapterID,
		&sessionID,
		&turnID,
		&event.Route.ConnectionID,
		&event.Route.ProviderID,
		&event.Route.ProviderVersion,
		&event.Route.ConfigRevision,
		&event.Turn.Identity.TenantID,
		&event.Turn.Identity.AgentID,
		&event.Turn.Identity.UserID,
		&actorDigest,
		&event.ContentHash,
		&payload,
		&event.State,
		&event.AttemptCount,
		&nextAttempt,
		&receiptJSON,
		&errorCode,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return Event{}, err
	}
	event.Turn.Identity.ConnectionID = event.Route.ConnectionID
	event.Turn.Identity.SessionID = sessionID
	event.Turn.Identity.TurnID = turnID
	event.Turn.ActorDigest = actorDigest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &event.Turn); err != nil {
			return Event{}, fmt.Errorf("decode journal payload: %w", err)
		}
	}
	event.Turn.ActorDigest = actorDigest
	if len(receiptJSON) > 0 {
		if err := json.Unmarshal(receiptJSON, &event.Receipt); err != nil {
			return Event{}, fmt.Errorf("decode journal receipt: %w", err)
		}
	}
	if nextAttempt.Valid {
		parsed, err := parseTime(nextAttempt.String)
		if err != nil {
			return Event{}, err
		}
		event.NextAttemptAt = &parsed
	}
	event.ErrorCode = errorCode.String
	event.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Event{}, err
	}
	event.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Event{}, err
	}
	return event, nil
}

func eventByScope(ctx context.Context, tx *sql.Tx, adapterID, connectionID string, identity model.IdentityScope) (Event, error) {
	return scanEvent(tx.QueryRowContext(ctx, selectEvent+`
		WHERE adapter_id = ? AND connection_id = ? AND tenant_id = ? AND agent_id = ?
		  AND user_id = ? AND session_id = ? AND turn_id = ?`,
		adapterID, connectionID, identity.TenantID, identity.AgentID, identity.UserID, identity.SessionID, identity.TurnID))
}

func requireChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func finishLatestAttempt(ctx context.Context, tx *sql.Tx, eventID string, state State, errorCode string, finishedAt time.Time) error {
	var storedError any
	if errorCode != "" {
		storedError = errorCode
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET finished_at = ?, delivery_state = ?, error_code = ?
		WHERE id = (
			SELECT id FROM delivery_attempts
			WHERE event_id = ? AND finished_at IS NULL
			ORDER BY id DESC LIMIT 1
		)`, formatTime(finishedAt), state, storedError, eventID)
	if err != nil {
		return fmt.Errorf("finish delivery attempt: %w", err)
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashParts(parts ...string) string {
	return hashBytes([]byte(strings.Join(parts, "\x00")))
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse journal timestamp: %w", err)
	}
	return parsed, nil
}
