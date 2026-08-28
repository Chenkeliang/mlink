package model

import (
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
