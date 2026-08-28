package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

type QueueStatus struct {
	Queued    int `json:"queued"`
	Retrying  int `json:"retrying"`
	Ambiguous int `json:"ambiguous"`
	Permanent int `json:"permanent_failure"`
}

func (s *Store) QueueSummary(ctx context.Context) (QueueStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, count(*) FROM journal_events GROUP BY state`)
	if err != nil {
		return QueueStatus{}, fmt.Errorf("summarize journal queue: %w", err)
	}
	defer rows.Close()
	var summary QueueStatus
	for rows.Next() {
		var state State
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return QueueStatus{}, err
		}
		switch state {
		case StateQueued, StateDispatching:
			summary.Queued += count
		case StateRetryableFailed:
			summary.Retrying += count
		case StateAmbiguous:
			summary.Ambiguous += count
		case StatePermanentFailed:
			summary.Permanent += count
		}
	}
	return summary, rows.Err()
}

func (s *Store) LatestAdapterActivity(ctx context.Context, adapterID string) (time.Time, error) {
	if adapterID == "" {
		return time.Time{}, errors.New("adapter ID is required")
	}
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT max(activity_at) FROM (
			SELECT created_at AS activity_at FROM journal_events WHERE adapter_id = ?
			UNION ALL
			SELECT occurred_at AS activity_at FROM turn_fragments WHERE adapter_id = ?
		)`, adapterID, adapterID).Scan(&raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("read latest Adapter activity: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, fs.ErrNotExist
	}
	return parseTime(raw.String)
}
