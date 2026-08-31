package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
)

var (
	ErrBindingRevoked  = errors.New("identity binding is revoked")
	ErrBindingConflict = errors.New("identity binding is ambiguous")
)

type bindingMatch struct {
	PrincipalID string
	Matched     bool
	Revoked     bool
}

func (bindings BindingSet) match(key []byte, context ExternalContext) (bindingMatch, error) {
	candidates := []struct {
		value string
		kinds map[string]bool
	}{
		{value: context.AlternateSubject, kinds: map[string]bool{"union_id": true}},
		{value: context.PrimarySubject, kinds: map[string]bool{"user_id": true, "open_id": true}},
	}
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		incoming := bindingDigest(key, context.Source, []byte(candidate.value))
		var result bindingMatch
		for _, binding := range bindings.Values {
			if binding.Source != context.Source || !candidate.kinds[binding.Kind] {
				continue
			}
			stored := bindingDigest(key, binding.Source, binding.Value)
			matched := subtle.ConstantTimeCompare(incoming, stored) == 1
			wipeDigest(stored)
			if !matched {
				continue
			}
			if result.Matched && (result.PrincipalID != binding.PrincipalID || result.Revoked != binding.Revoked) {
				wipeDigest(incoming)
				return bindingMatch{}, ErrBindingConflict
			}
			result = bindingMatch{PrincipalID: binding.PrincipalID, Matched: true, Revoked: binding.Revoked}
		}
		wipeDigest(incoming)
		if result.Matched {
			return result, nil
		}
	}
	return bindingMatch{}, nil
}

func bindingDigest(key []byte, source string, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("binding\x00" + source + "\x00"))
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func BindingFingerprint(key []byte, source string, value []byte) string {
	if len(key) != 32 || source == "" || len(value) == 0 {
		return ""
	}
	digest := bindingDigest(key, source, value)
	defer wipeDigest(digest)
	return "bind_" + hex.EncodeToString(digest[:8])
}

func wipeDigest(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
