package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ScopeKind string

const (
	ScopeUser  ScopeKind = "user"
	ScopeAgent ScopeKind = "agent"
)

type IdentityScope struct {
	ConnectionID string `json:"connection_id"`
	TenantID     string `json:"tenant_id"`
	UserID       string `json:"user_id"`
	AgentID      string `json:"agent_id"`
	SessionID    string `json:"session_id,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
}

func (s IdentityScope) ValidateForRecall() error {
	return validateIdentityFields(
		struct{ name, value string }{"tenant_id", s.TenantID},
		struct{ name, value string }{"user_id", s.UserID},
		struct{ name, value string }{"agent_id", s.AgentID},
	)
}

func (s IdentityScope) ValidateForCapture() error {
	if err := s.ValidateForRecall(); err != nil {
		return err
	}
	return validateIdentityFields(
		struct{ name, value string }{"session_id", s.SessionID},
		struct{ name, value string }{"turn_id", s.TurnID},
	)
}

func validateIdentityFields(fields ...struct{ name, value string }) error {
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: missing %s", ErrInvalidIdentity, field.name)
		}
	}
	return nil
}

type Message struct {
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Turn struct {
	Identity IdentityScope `json:"identity"`
	Messages []Message     `json:"messages"`
}

type RecallRequest struct {
	Identity           IdentityScope `json:"identity"`
	Query              string        `json:"query"`
	MaxItems           int           `json:"max_items"`
	IncludeAgentShared bool          `json:"include_agent_shared"`
}

type ContextItem struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Scope     ScopeKind  `json:"scope"`
	Text      string     `json:"text"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	Score     *float64   `json:"score,omitempty"`
	Source    string     `json:"source"`
}

type ContextBundle struct {
	Items    []ContextItem `json:"items"`
	Partial  bool          `json:"partial"`
	Warnings []string      `json:"warnings,omitempty"`
}

type WriteReceipt struct {
	AcceptedIDs []string `json:"accepted_ids"`
}

var ErrInvalidIdentity = errors.New("invalid identity scope")
