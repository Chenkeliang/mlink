package main

import (
	"bytes"
	"context"
	"testing"

	"mlink/internal/cli"
)

func TestRunDependenciesStartTencentDBProvider(t *testing.T) {
	calls := 0
	deps := runDependencies(&bytes.Buffer{}, &bytes.Buffer{}, func(context.Context) error {
		calls++
		return nil
	})
	if code := cli.Run(context.Background(), []string{"provider", "run", "tencentdb"}, deps); code != 0 {
		t.Fatalf("Run() code = %d", code)
	}
	if calls != 1 {
		t.Fatalf("serve calls = %d, want 1", calls)
	}
}
