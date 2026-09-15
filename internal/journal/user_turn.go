package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mlink/internal/connection"
	"mlink/internal/model"
	"strings"
	"time"
)

func observationID(adapter string, i model.IdentityScope) string {
	b := sha256.Sum256([]byte(strings.Join([]string{adapter, i.ConnectionID, i.TenantID, i.AgentID, i.UserID, i.SessionID, i.TurnID}, "\x00")))
	return "obs_" + hex.EncodeToString(b[:16])
}

func (s *Store) ClaimObservation(ctx context.Context, adapter string, route connection.RouteKey, turn model.UserTurn) (string, bool, error) {
	id := observationID(adapter, turn.Identity)
	raw, err := json.Marshal(turn)
	if err != nil {
		return "", false, err
	}
	i := turn.Identity
	routeJSON, _ := json.Marshal(route)
	r, err := s.db.ExecContext(ctx, `INSERT INTO user_turn_observations(observation_id,adapter_id,connection_id,tenant_id,agent_id,user_id,session_id,turn_id,turn_json,route_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(observation_id) DO NOTHING`, id, adapter, i.ConnectionID, i.TenantID, i.AgentID, i.UserID, i.SessionID, i.TurnID, raw, routeJSON, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()))
	if err != nil {
		return "", false, err
	}
	n, err := r.RowsAffected()
	return id, n == 1, err
}

func (s *Store) SaveObservation(ctx context.Context, id string, receipt model.ObservationReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_turn_observations SET receipt_json=?,updated_at=? WHERE observation_id=?`, raw, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) EndObservation(ctx context.Context, adapter string, i model.IdentityScope) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_turn_observations SET active=0,updated_at=? WHERE observation_id=?`, formatTime(time.Now().UTC()), observationID(adapter, i))
	return err
}

func (s *Store) PreviousMessages(ctx context.Context, adapter string, i model.IdentityScope) ([]model.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT completed_json FROM user_turn_observations WHERE adapter_id=? AND connection_id=? AND tenant_id=? AND agent_id=? AND user_id=? AND session_id=? AND turn_id<>? AND completed_json IS NOT NULL ORDER BY updated_at DESC LIMIT 3`, adapter, i.ConnectionID, i.TenantID, i.AgentID, i.UserID, i.SessionID, i.TurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups [][]model.Message
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m []model.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		groups = append(groups, m)
	}
	var out []model.Message
	for n := len(groups) - 1; n >= 0; n-- {
		for _, m := range groups[n] {
			v := []rune(m.Content)
			if len(v) > 4000 {
				m.Content = string(v[:4000])
			}
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *Store) CompleteObservation(ctx context.Context, adapter string, i model.IdentityScope, messages []model.Message) error {
	id := observationID(adapter, i)
	if len(messages) == 1 && messages[0].Role == "assistant" {
		var raw []byte
		if err := s.db.QueryRowContext(ctx, `SELECT turn_json FROM user_turn_observations WHERE observation_id=?`, id).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var t model.UserTurn
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		messages = append([]model.Message{t.Message}, messages...)
	}
	bounded := make([]model.Message, 0, len(messages))
	for _, m := range messages {
		v := []rune(m.Content)
		if len(v) > 4000 {
			m.Content = string(v[:4000])
		}
		bounded = append(bounded, m)
	}
	raw, err := json.Marshal(bounded)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE user_turn_observations SET active=0,completed_json=?,updated_at=? WHERE observation_id=?`, raw, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) EndSessionObservations(ctx context.Context, adapter string, i model.IdentityScope) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_turn_observations SET active=0 WHERE adapter_id=? AND connection_id=? AND tenant_id=? AND agent_id=? AND user_id=? AND session_id=?`, adapter, i.ConnectionID, i.TenantID, i.AgentID, i.UserID, i.SessionID)
	return err
}

type ActiveObservation struct {
	Route connection.RouteKey
	Turn  model.UserTurn
}

func (s *Store) ActiveObservations(ctx context.Context) ([]ActiveObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT route_json,turn_json FROM user_turn_observations WHERE active=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveObservation
	for rows.Next() {
		var a, b []byte
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		var o ActiveObservation
		if err := json.Unmarshal(a, &o.Route); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &o.Turn); err != nil {
			return nil, err
		}
		o.Turn.Correction = false
		o.Turn.Previous = nil
		out = append(out, o)
	}
	return out, rows.Err()
}
