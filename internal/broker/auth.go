package broker

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"mlink/internal/connection"
	"mlink/internal/identity"
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
}

type Authorizer struct {
	Resolver identity.Resolver
	Grants   []Grant
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

func cloneGrant(grant Grant) Grant {
	grant.TokenDigest = append([]byte(nil), grant.TokenDigest...)
	return grant
}
