package doctor

import (
	"bytes"
	"errors"
	"testing"

	"mlink/internal/config"
)

func TestReportDistinguishesPendingActionFromFailure(t *testing.T) {
	report := Report{Checks: []Check{
		{ID: "codex.hook", State: StatePendingAction, Code: "awaiting_trust"},
		{ID: "broker.socket", State: StateFailed, Code: "socket_unavailable"},
	}}
	if report.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1", report.ExitCode())
	}
	if report.Checks[0].Code == report.Checks[1].Code {
		t.Fatal("pending trust and Broker failure were collapsed")
	}
}

func TestReportPendingActionUsesExitFour(t *testing.T) {
	report := Report{Checks: []Check{{ID: "codex.hook", State: StatePendingAction, Code: "awaiting_trust"}}}
	if report.ExitCode() != 4 {
		t.Fatalf("exit = %d, want 4", report.ExitCode())
	}
}

func TestReportHealthyUsesExitZero(t *testing.T) {
	report := Report{Checks: []Check{{ID: "broker.socket", State: StatePassed, Code: "reachable"}}}
	if report.ExitCode() != 0 {
		t.Fatalf("exit = %d, want 0", report.ExitCode())
	}
}

func TestIdentityChecksDistinguishBindingFailureFromSpaceState(t *testing.T) {
	cfg := config.Config{
		Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner"}, "hermes-private": {ID: "hermes-private"}, "hermes-groups": {ID: "hermes-groups"},
		},
		Bindings: map[string]config.BindingRef{"owner-feishu-union-1": {ID: "owner-feishu-union-1", PrincipalID: "owner", Status: config.BindingActive}},
	}
	checks := IdentityChecks(cfg, bytes.Repeat([]byte{0x2a}, 32), errors.New("binding missing"))
	if len(checks) != 5 || checks[0].ID != "identity.key" || checks[0].Code != "active" || checks[1].ID != "identity.owner" || checks[1].Code != "binding_missing" {
		t.Fatalf("checks = %#v", checks)
	}
	for _, check := range checks[2:] {
		if check.State != StatePassed {
			t.Fatalf("space check = %#v", check)
		}
	}
}
