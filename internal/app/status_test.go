package app

import (
	"context"
	"io/fs"
	"testing"

	"mlink/internal/layout"
)

func TestStatusReportsUninstalledWithoutError(t *testing.T) {
	service := Service{
		Paths:  layout.Paths{Config: "/Users/test/.mlink/config.yaml"},
		Target: newMemoryTarget(nil),
		Ledger: newMemoryLedger(),
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || status.ActivePlanID != "" || len(status.Adapters) != 0 {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusReadsActiveConnectionAndAdapters(t *testing.T) {
	ledger := newMemoryLedger()
	ledger.activePlanID = "plan_active"
	target := newMemoryTarget(map[string]memoryFile{
		"/Users/test/.mlink/config.yaml": {
			content: []byte("schema_version: 1\nactive_connection_id: local\nadapters:\n  codex:\n    id: codex\n    enabled: true\n    connection_id: local\n  pi:\n    id: pi\n    enabled: false\n    connection_id: local\n"),
			mode:    fs.FileMode(0o600),
		},
	})
	service := Service{Paths: layout.Paths{Config: "/Users/test/.mlink/config.yaml"}, Target: target, Ledger: ledger}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.ActivePlanID != "plan_active" || status.ConnectionID != "local" || !status.Adapters[Codex] || status.Adapters[Pi] {
		t.Fatalf("status = %#v", status)
	}
}
