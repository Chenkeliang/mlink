package main

import (
	"errors"
	"testing"
)

func TestDispatchRunsOnlyExactTencentDBProviderCommand(t *testing.T) {
	calls := 0
	serve := func() error {
		calls++
		return nil
	}
	if err := dispatch([]string{"provider", "run", "tencentdb"}, serve); err != nil {
		t.Fatalf("dispatch() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("serve calls = %d, want 1", calls)
	}
	for _, args := range [][]string{
		nil,
		{"provider", "run"},
		{"provider", "run", "tencentdb", "--unsafe"},
		{"provider", "run", "other"},
		{"doctor"},
	} {
		if err := dispatch(args, serve); err == nil {
			t.Fatalf("dispatch(%q) error = nil, want rejection", args)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid commands invoked serve; calls = %d", calls)
	}
}

func TestDispatchReturnsProviderServerFailure(t *testing.T) {
	want := errors.New("serve failed")
	if err := dispatch([]string{"provider", "run", "tencentdb"}, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("dispatch() error = %v, want %v", err, want)
	}
}
