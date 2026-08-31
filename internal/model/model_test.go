package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIdentityScopeValidateForRecall(t *testing.T) {
	valid := IdentityScope{
		TenantID: "team-a",
		UserID:   "user-a",
		AgentID:  "agent-a",
	}

	tests := []struct {
		name      string
		mutate    func(*IdentityScope)
		wantField string
		wantError bool
	}{
		{name: "complete identity", mutate: func(*IdentityScope) {}, wantError: false},
		{name: "missing tenant", mutate: func(s *IdentityScope) { s.TenantID = "" }, wantField: "tenant_id", wantError: true},
		{name: "missing user", mutate: func(s *IdentityScope) { s.UserID = "" }, wantField: "user_id", wantError: true},
		{name: "missing agent", mutate: func(s *IdentityScope) { s.AgentID = "" }, wantField: "agent_id", wantError: true},
		{name: "blank user", mutate: func(s *IdentityScope) { s.UserID = "  " }, wantField: "user_id", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := valid
			tt.mutate(&scope)

			err := scope.ValidateForRecall()
			if !tt.wantError {
				if err != nil {
					t.Fatalf("ValidateForRecall() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("ValidateForRecall() error = %v, want ErrInvalidIdentity", err)
			}
			if !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("ValidateForRecall() error = %q, want missing field %q", err, tt.wantField)
			}
		})
	}
}

func TestIdentityScopeValidateForCapture(t *testing.T) {
	valid := IdentityScope{
		TenantID:  "team-a",
		UserID:    "user-a",
		AgentID:   "agent-a",
		SessionID: "session-a",
		TurnID:    "turn-a",
	}

	tests := []struct {
		name      string
		mutate    func(*IdentityScope)
		wantField string
		wantError bool
	}{
		{name: "complete identity", mutate: func(*IdentityScope) {}, wantError: false},
		{name: "missing tenant", mutate: func(s *IdentityScope) { s.TenantID = "" }, wantField: "tenant_id", wantError: true},
		{name: "missing user", mutate: func(s *IdentityScope) { s.UserID = "" }, wantField: "user_id", wantError: true},
		{name: "missing agent", mutate: func(s *IdentityScope) { s.AgentID = "" }, wantField: "agent_id", wantError: true},
		{name: "missing session", mutate: func(s *IdentityScope) { s.SessionID = "" }, wantField: "session_id", wantError: true},
		{name: "missing turn", mutate: func(s *IdentityScope) { s.TurnID = "" }, wantField: "turn_id", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := valid
			tt.mutate(&scope)

			err := scope.ValidateForCapture()
			if !tt.wantError {
				if err != nil {
					t.Fatalf("ValidateForCapture() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("ValidateForCapture() error = %v, want ErrInvalidIdentity", err)
			}
			if !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("ValidateForCapture() error = %q, want missing field %q", err, tt.wantField)
			}
		})
	}
}

func TestWriteReceiptJSONContract(t *testing.T) {
	receipt := WriteReceipt{
		ReceiptID:    "request-7",
		State:        WriteAccepted,
		ProviderRefs: []string{"memory-a"},
		ReplaySafe:   false,
		Warnings:     []string{"visible after extraction"},
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	got := string(raw)
	for _, field := range []string{
		`"receipt_id":"request-7"`,
		`"state":"accepted"`,
		`"provider_refs":["memory-a"]`,
		`"replay_safe":false`,
		`"warnings":["visible after extraction"]`,
	} {
		if !strings.Contains(got, field) {
			t.Fatalf("JSON = %s, want field %s", got, field)
		}
	}
}

func TestTurnFragmentJSONDoesNotExposeCanonicalUserOverride(t *testing.T) {
	fragment := TurnFragment{
		AdapterID: "hermes",
		Identity:  AdapterIdentity{Source: "feishu", SourceSubject: "ou_stable", DisplayName: "User"},
		SessionID: "session",
		TurnID:    "turn",
		Role:      "user",
		Content:   "hello",
	}
	data, err := json.Marshal(fragment)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user_id") {
		t.Fatalf("fragment permits canonical user override: %s", data)
	}
}

func TestTurnActorDigestIsLocalOnly(t *testing.T) {
	turn := Turn{
		Identity:    IdentityScope{TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "turn"},
		ActorDigest: "actor_safe",
		Messages:    []Message{{Role: "user", Content: "hello"}},
	}
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "actor_safe") || strings.Contains(string(raw), "actor_digest") {
		t.Fatalf("actor digest leaked into Provider turn: %s", raw)
	}
}
