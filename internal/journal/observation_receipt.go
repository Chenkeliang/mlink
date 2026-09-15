package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mlink/internal/model"
)

func (s *Store) ObservationReceipt(ctx context.Context, adapter string, i model.IdentityScope) (model.ObservationReceipt, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT receipt_json FROM user_turn_observations WHERE observation_id=?`, observationID(adapter, i)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ObservationReceipt{}, nil
	}
	if err != nil {
		return model.ObservationReceipt{}, err
	}
	if len(raw) == 0 {
		return model.ObservationReceipt{CorrectionStatus: "pending"}, nil
	}
	var r model.ObservationReceipt
	err = json.Unmarshal(raw, &r)
	return r, err
}
