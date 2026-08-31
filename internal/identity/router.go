package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"

	"mlink/internal/config"
)

var (
	ErrGroupIdentityMissing = errors.New("stable group identity is missing")
	ErrUnsupportedContext   = errors.New("external identity context is unsupported")
)

type Router struct {
	NamespaceID string
	Key         []byte
	Connections map[string]config.Connection
	Spaces      map[string]config.MemorySpace
	Principals  map[string]config.Principal
	Adapters    map[string]config.Adapter
	Bindings    BindingSet
	Hermes      config.HermesRouting
}

func (router Router) ResolveFixed(adapterID, sessionID string) (ResolvedIdentity, error) {
	if !router.valid() || adapterID == "" || sessionID == "" {
		return ResolvedIdentity{}, ErrIdentityMissing
	}
	adapter, exists := router.Adapters[adapterID]
	if !exists || !adapter.Enabled || adapter.SpaceID == "" {
		return ResolvedIdentity{}, ErrUnsupportedContext
	}
	space, principal, err := router.fixedSpace(adapter.SpaceID)
	if err != nil {
		return ResolvedIdentity{}, err
	}
	return router.resolved(space, principal.CanonicalUserID, sessionID, ""), nil
}

func (router Router) ResolveHermesIntent(context ExternalContext) (RouteIntent, error) {
	if !router.valid() || context.Source != "feishu" {
		return RouteIntent{}, ErrUnsupportedContext
	}
	sessionID := router.sessionID(context)
	actorDigest := router.actorDigest(context)
	switch context.ChatType {
	case "group":
		if context.ChatID == "" {
			return RouteIntent{}, ErrGroupIdentityMissing
		}
		return RouteIntent{
			Kind: RouteGroup, PrincipalFingerprint: "prn_" + router.hmacBase32("principal-group\x00"+router.NamespaceID+"\x00"+context.Source+"\x00"+context.ChatID),
			SessionID: sessionID, ActorDigest: actorDigest,
		}, nil
	case "dm":
		if context.ChatID == "" || context.AlternateSubject == "" && context.PrimarySubject == "" {
			return RouteIntent{}, ErrIdentityMissing
		}
		match, err := router.Bindings.match(router.Key, context)
		if err != nil {
			return RouteIntent{}, err
		}
		if match.Revoked {
			return RouteIntent{}, ErrBindingRevoked
		}
		if match.Matched {
			owner, exists := router.Principals["owner"]
			if !exists || owner.CanonicalUserID == "" || match.PrincipalID != owner.ID {
				return RouteIntent{}, ErrBindingConflict
			}
			return RouteIntent{Kind: RouteOwner, SessionID: sessionID, ActorDigest: actorDigest, IncludeAgentShared: true}, nil
		}
		subject := stableSubject(context)
		return RouteIntent{
			Kind: RoutePrivate, PrincipalFingerprint: "prn_" + router.hmacBase32("principal-private\x00"+router.NamespaceID+"\x00"+context.Source+"\x00"+subject),
			SessionID: sessionID, ActorDigest: actorDigest,
		}, nil
	default:
		return RouteIntent{}, ErrUnsupportedContext
	}
}

func (router Router) ResolveHermes(context ExternalContext) (ResolvedIdentity, error) {
	if !router.valid() || context.Source != "feishu" {
		return ResolvedIdentity{}, ErrUnsupportedContext
	}
	switch context.ChatType {
	case "group":
		if context.ChatID == "" {
			return ResolvedIdentity{}, ErrGroupIdentityMissing
		}
		space, err := router.dynamicSpace(router.Hermes.GroupSpaceID, config.PolicyGroupHMAC)
		if err != nil {
			return ResolvedIdentity{}, err
		}
		userID := "grp_" + router.hmacBase32("group\x00"+router.NamespaceID+"\x00"+context.Source+"\x00"+context.ChatID)
		return router.resolved(space, userID, router.sessionID(context), router.actorDigest(context)), nil
	case "dm":
		if context.ChatID == "" || context.AlternateSubject == "" && context.PrimarySubject == "" {
			return ResolvedIdentity{}, ErrIdentityMissing
		}
		match, err := router.Bindings.match(router.Key, context)
		if err != nil {
			return ResolvedIdentity{}, err
		}
		if match.Revoked {
			return ResolvedIdentity{}, ErrBindingRevoked
		}
		if match.Matched {
			space, principal, err := router.fixedSpace(router.Hermes.OwnerSpaceID)
			if err != nil {
				return ResolvedIdentity{}, err
			}
			if principal.ID != match.PrincipalID {
				return ResolvedIdentity{}, ErrBindingConflict
			}
			return router.resolved(space, principal.CanonicalUserID, router.sessionID(context), router.actorDigest(context)), nil
		}
		space, err := router.dynamicSpace(router.Hermes.PrivateSpaceID, config.PolicyExternalHMAC)
		if err != nil {
			return ResolvedIdentity{}, err
		}
		subject := stableSubject(context)
		userID := "usr_" + router.hmacBase32("external-user\x00"+router.NamespaceID+"\x00"+context.Source+"\x00"+subject)
		return router.resolved(space, userID, router.sessionID(context), router.actorDigest(context)), nil
	default:
		return ResolvedIdentity{}, ErrUnsupportedContext
	}
}

func (router Router) fixedSpace(spaceID string) (config.MemorySpace, config.Principal, error) {
	space, exists := router.Spaces[spaceID]
	if !exists || space.PrincipalPolicy != config.PolicyFixed {
		return config.MemorySpace{}, config.Principal{}, ErrUnsupportedContext
	}
	principal, exists := router.Principals[space.PrincipalID]
	if !exists || principal.CanonicalUserID == "" {
		return config.MemorySpace{}, config.Principal{}, ErrUnsupportedContext
	}
	if _, exists := router.Connections[space.ConnectionID]; !exists {
		return config.MemorySpace{}, config.Principal{}, ErrUnsupportedContext
	}
	return space, principal, nil
}

func (router Router) dynamicSpace(spaceID string, policy config.PrincipalPolicy) (config.MemorySpace, error) {
	space, exists := router.Spaces[spaceID]
	if !exists || space.PrincipalPolicy != policy {
		return config.MemorySpace{}, ErrUnsupportedContext
	}
	if _, exists := router.Connections[space.ConnectionID]; !exists {
		return config.MemorySpace{}, ErrUnsupportedContext
	}
	return space, nil
}

func (router Router) resolved(space config.MemorySpace, userID, sessionID, actorDigest string) ResolvedIdentity {
	return ResolvedIdentity{
		ConnectionID:       space.ConnectionID,
		SpaceID:            space.ID,
		TenantID:           space.TenantID,
		AgentID:            space.AgentID,
		UserID:             userID,
		SessionID:          sessionID,
		IncludeAgentShared: space.IncludeAgentShared,
		ActorDigest:        actorDigest,
	}
}

func (router Router) sessionID(context ExternalContext) string {
	return "ses_" + router.hmacBase32("session\x00"+context.Source+"\x00"+context.ChatID+"\x00"+context.ThreadID)
}

func (router Router) actorDigest(context ExternalContext) string {
	subject := stableSubject(context)
	if subject == "" {
		return ""
	}
	return "actor_" + router.hmacBase32("actor\x00"+context.Source+"\x00"+subject)
}

func stableSubject(context ExternalContext) string {
	if context.AlternateSubject != "" {
		return context.AlternateSubject
	}
	return context.PrimarySubject
}

func (router Router) valid() bool {
	return router.NamespaceID != "" && len(router.Key) == 32
}

func (router Router) hmacBase32(value string) string {
	return hmacBase32(router.Key, value)
}

func hmacBase32(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return strings.ToLower(encoded[:26])
}

func CanonicalTurnID(key []byte, actorDigest, incomingTurnID string) string {
	if len(key) != 32 || actorDigest == "" || incomingTurnID == "" {
		return ""
	}
	return "turn_" + hmacBase32(key, "turn\x00"+actorDigest+"\x00"+incomingTurnID)
}
