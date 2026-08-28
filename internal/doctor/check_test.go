package doctor

import "testing"

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
