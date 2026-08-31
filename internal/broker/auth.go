package broker

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/journal"
)

var (
	ErrUnauthorized = errors.New("broker request is unauthorized")
	ErrForbidden    = errors.New("broker identity is forbidden")
)

type IdentityMode string

const (
	IdentityFixed     IdentityMode = "fixed"
	IdentityDelegated IdentityMode = "delegated"
)

type Grant struct {
	TokenDigest        []byte
	AdapterID          string
	Mode               IdentityMode
	Source             string
	Route              connection.RouteKey
	TenantID           string
	AgentID            string
	UserID             string
	IncludeAgentShared bool
	FixedSpaceID       string
	AllowedSpaceIDs    map[string]bool
}

type Authorizer struct {
	Resolver      identity.Resolver
	Grants        []Grant
	Router        *identity.Router
	ConnectionID  string
	ControlPlane  *config.ControlPlane
	DynamicAgents DynamicAgentProvisioner
}

type DynamicAgentProvisioner interface {
	ResolveOrCreate(context.Context, controlplane.PrincipalIntent) (journal.PrincipalAgent, error)
}

type Authorization struct {
	Grant              Grant
	Route              connection.RouteKey
	Identity           identity.ResolvedIdentity
	IncludeAgentShared bool
}

func (a Authorizer) GrantFor(token, adapterID string, local bool) (Grant, error) {
	if strings.TrimSpace(adapterID) == "" {
		return Grant{}, ErrUnauthorized
	}
	if local {
		for _, grant := range a.Grants {
			if grant.AdapterID == adapterID && grant.Mode == IdentityFixed {
				return cloneGrant(grant), nil
			}
		}
		return Grant{}, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	matched := -1
	for i, grant := range a.Grants {
		validDigest := len(grant.TokenDigest) == sha256.Size && subtle.ConstantTimeCompare(digest[:], grant.TokenDigest) == 1
		if validDigest {
			matched = i
		}
	}
	if matched < 0 {
		return Grant{}, ErrUnauthorized
	}
	grant := a.Grants[matched]
	if grant.AdapterID != adapterID {
		return Grant{}, ErrForbidden
	}
	return cloneGrant(grant), nil
}

func (a Authorizer) CanonicalUser(grant Grant, source, subject, directUserID string) (string, error) {
	if directUserID != "" {
		return "", ErrForbidden
	}
	switch grant.Mode {
	case IdentityFixed:
		if grant.UserID == "" {
			return "", ErrForbidden
		}
		return a.Resolver.ResolveFixed(grant.UserID)
	case IdentityDelegated:
		if source != grant.Source || strings.TrimSpace(subject) == "" {
			return "", ErrForbidden
		}
		userID, err := a.Resolver.ResolveDelegated(source, subject)
		if err != nil {
			return "", ErrForbidden
		}
		return userID, nil
	default:
		return "", ErrForbidden
	}
}

func (a Authorizer) Resolve(ctx context.Context, grant Grant, external identity.ExternalContext, directUserID, sessionID string) (Authorization, error) {
	if directUserID != "" {
		return Authorization{}, ErrForbidden
	}
	if a.ControlPlane != nil {
		return a.resolveControlPlane(ctx, grant, external, sessionID)
	}
	return a.resolveLegacy(grant, external, directUserID, sessionID)
}

func (a Authorizer) resolveLegacy(grant Grant, external identity.ExternalContext, directUserID, sessionID string) (Authorization, error) {
	if a.Router == nil {
		userID, err := a.CanonicalUser(grant, external.Source, stableExternalSubject(external), directUserID)
		if err != nil {
			return Authorization{}, err
		}
		resolved := identity.ResolvedIdentity{
			ConnectionID: grant.Route.ConnectionID, TenantID: grant.TenantID, AgentID: grant.AgentID,
			UserID: userID, SessionID: sessionID, IncludeAgentShared: grant.IncludeAgentShared,
		}
		return Authorization{Grant: grant, Route: grant.Route, Identity: resolved, IncludeAgentShared: grant.IncludeAgentShared}, nil
	}
	var resolved identity.ResolvedIdentity
	var err error
	switch grant.Mode {
	case IdentityFixed:
		resolved, err = a.Router.ResolveFixed(grant.AdapterID, sessionID)
	case IdentityDelegated:
		if external.Source != grant.Source {
			return Authorization{}, ErrForbidden
		}
		resolved, err = a.Router.ResolveHermes(external)
	default:
		err = ErrForbidden
	}
	if err != nil {
		return Authorization{}, ErrForbidden
	}
	if len(grant.AllowedSpaceIDs) != 0 && !grant.AllowedSpaceIDs[resolved.SpaceID] {
		return Authorization{}, ErrForbidden
	}
	connectionConfig, exists := a.Router.Connections[resolved.ConnectionID]
	if !exists {
		return Authorization{}, ErrForbidden
	}
	route := connection.RouteKey{
		ConnectionID: connectionConfig.ID, ProviderID: connectionConfig.ProviderID,
		ProviderVersion: connectionConfig.ProviderVersion, ConfigRevision: connectionConfig.ConfigRevision,
	}
	return Authorization{Grant: grant, Route: route, Identity: resolved, IncludeAgentShared: resolved.IncludeAgentShared}, nil
}

func (a Authorizer) resolveControlPlane(ctx context.Context, grant Grant, external identity.ExternalContext, sessionID string) (Authorization, error) {
	if a.Router == nil || strings.TrimSpace(a.ConnectionID) == "" || a.ControlPlane == nil {
		return Authorization{}, ErrForbidden
	}
	control := a.ControlPlane
	connectionConfig, exists := a.Router.Connections[a.ConnectionID]
	if !exists || connectionConfig.ProviderID != control.ProviderID {
		return Authorization{}, ErrForbidden
	}
	intent := identity.RouteIntent{Kind: identity.RouteOwner, SessionID: sessionID, IncludeAgentShared: true}
	if grant.Mode == IdentityDelegated {
		if external.Source != grant.Source {
			return Authorization{}, ErrForbidden
		}
		resolved, err := a.Router.ResolveHermesIntent(external)
		if err != nil {
			return Authorization{}, ErrForbidden
		}
		intent = resolved
	} else if grant.Mode != IdentityFixed {
		return Authorization{}, ErrForbidden
	}

	resolved := identity.ResolvedIdentity{
		ConnectionID: a.ConnectionID, SpaceID: "owner", TenantID: control.OwnerTeamID,
		AgentID: control.OwnerAgentID, UserID: control.OwnerUserID, SessionID: intent.SessionID,
		IncludeAgentShared: intent.Kind == identity.RouteOwner, ActorDigest: intent.ActorDigest,
	}
	if intent.Kind != identity.RouteOwner {
		if a.DynamicAgents == nil {
			return Authorization{}, ErrForbidden
		}
		displayLabel := "Feishu DM"
		if intent.Kind == identity.RouteGroup {
			displayLabel = "Feishu Group"
		}
		mapping, err := a.DynamicAgents.ResolveOrCreate(ctx, controlplane.PrincipalIntent{
			Fingerprint: intent.PrincipalFingerprint, RouteKind: string(intent.Kind), DisplayLabel: displayLabel,
		})
		if err != nil || mapping.State != "active" || mapping.RouteKind != string(intent.Kind) ||
			mapping.BackendUserID != control.OwnerUserID || mapping.BackendTeamID != control.OwnerTeamID || strings.TrimSpace(mapping.BackendAgentID) == "" {
			return Authorization{}, ErrForbidden
		}
		resolved.AgentID = mapping.BackendAgentID
		resolved.IncludeAgentShared = false
		resolved.SpaceID = "hermes-private"
		if intent.Kind == identity.RouteGroup {
			resolved.SpaceID = "hermes-groups"
		}
	}
	if len(grant.AllowedSpaceIDs) != 0 && !grant.AllowedSpaceIDs[resolved.SpaceID] {
		return Authorization{}, ErrForbidden
	}
	route := connection.RouteKey{
		ConnectionID: connectionConfig.ID, ProviderID: connectionConfig.ProviderID,
		ProviderVersion: connectionConfig.ProviderVersion, ConfigRevision: connectionConfig.ConfigRevision,
	}
	return Authorization{Grant: grant, Route: route, Identity: resolved, IncludeAgentShared: resolved.IncludeAgentShared}, nil
}

func (a Authorizer) CanonicalTurnID(actorDigest, incomingTurnID string) string {
	if a.Router == nil || actorDigest == "" {
		return incomingTurnID
	}
	return identity.CanonicalTurnID(a.Router.Key, actorDigest, incomingTurnID)
}

func stableExternalSubject(external identity.ExternalContext) string {
	if external.AlternateSubject != "" {
		return external.AlternateSubject
	}
	return external.PrimarySubject
}

func cloneGrant(grant Grant) Grant {
	grant.TokenDigest = append([]byte(nil), grant.TokenDigest...)
	if grant.AllowedSpaceIDs != nil {
		allowedSpaces := grant.AllowedSpaceIDs
		grant.AllowedSpaceIDs = make(map[string]bool, len(allowedSpaces))
		for id, allowed := range allowedSpaces {
			grant.AllowedSpaceIDs[id] = allowed
		}
	}
	return grant
}
