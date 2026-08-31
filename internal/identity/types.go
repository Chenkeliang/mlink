package identity

import "fmt"

type ExternalContext struct {
	Source           string
	ChatType         string
	ChatID           string
	ThreadID         string
	PrimarySubject   string
	AlternateSubject string
}

func (context ExternalContext) String() string {
	return fmt.Sprintf("ExternalContext{Source:%q ChatType:%q ChatID:<redacted> ThreadID:<redacted> PrimarySubject:<redacted> AlternateSubject:<redacted>}", context.Source, context.ChatType)
}

func (context ExternalContext) GoString() string { return context.String() }

type RouteKind string

const (
	RouteOwner   RouteKind = "owner"
	RoutePrivate RouteKind = "hermes-private"
	RouteGroup   RouteKind = "hermes-group"
)

type RouteIntent struct {
	Kind                 RouteKind
	PrincipalFingerprint string
	SessionID            string
	ActorDigest          string
	IncludeAgentShared   bool
}

type ResolvedIdentity struct {
	ConnectionID       string
	SpaceID            string
	TenantID           string
	AgentID            string
	UserID             string
	SessionID          string
	IncludeAgentShared bool
	ActorDigest        string
}

type BindingValue struct {
	RefID       string
	Source      string
	Kind        string
	Value       []byte
	PrincipalID string
	Revoked     bool
}

func (binding BindingValue) String() string {
	return fmt.Sprintf("BindingValue{RefID:%q Source:%q Kind:%q PrincipalID:%q Revoked:%t Value:<redacted>}", binding.RefID, binding.Source, binding.Kind, binding.PrincipalID, binding.Revoked)
}

func (binding BindingValue) GoString() string { return binding.String() }

type BindingSet struct {
	Values []BindingValue
}
