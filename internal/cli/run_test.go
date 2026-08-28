package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunPreservesTencentDBProviderCommand(t *testing.T) {
	calls := 0
	deps := Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		ServeTencentDB: func(context.Context) error {
			calls++
			return nil
		},
	}
	if code := Run(context.Background(), []string{"provider", "run", "tencentdb"}, deps); code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if calls != 1 {
		t.Fatalf("ServeTencentDB calls = %d, want 1", calls)
	}
}

func TestRunReportsProviderFailureWithoutLeakingDetails(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := Run(context.Background(), []string{"provider", "run", "tencentdb"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: stderr,
		ServeTencentDB: func(context.Context) error {
			return errors.New("token actual-secret rejected")
		},
	})
	if code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	if got := stderr.String(); got != "mlink provider command failed\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunRecognizesPlannedCommands(t *testing.T) {
	commands := [][]string{
		{"install"},
		{"status"},
		{"doctor"},
		{"backup"},
		{"uninstall"},
		{"broker", "serve"},
		{"hook", "codex"},
	}
	for _, args := range commands {
		stderr := &bytes.Buffer{}
		if code := Run(context.Background(), args, Dependencies{Stdout: &bytes.Buffer{}, Stderr: stderr}); code != 2 {
			t.Fatalf("Run(%q) code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "not implemented") {
			t.Fatalf("Run(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	stderr := &bytes.Buffer{}
	if code := Run(context.Background(), []string{"unknown"}, Dependencies{Stdout: &bytes.Buffer{}, Stderr: stderr}); code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if got := stderr.String(); got != "unsupported MLink command\n" {
		t.Fatalf("stderr = %q", got)
	}
}
