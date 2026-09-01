package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

func CheckpointAndVerify(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute Journal path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return errors.New("open Journal for checkpoint")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return errors.New("configure Journal checkpoint")
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil || busy != 0 {
		return fmt.Errorf("checkpoint Journal WAL: busy=%d", busy)
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("Journal quick_check failed")
	}
	return nil
}
