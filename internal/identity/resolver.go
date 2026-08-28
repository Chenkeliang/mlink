package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
)

var ErrIdentityMissing = errors.New("stable identity is missing")

type Resolver struct {
	NamespaceID string
	Key         []byte
}

func (r Resolver) ResolveFixed(userID string) (string, error) {
	if r.NamespaceID == "" || userID == "" {
		return "", ErrIdentityMissing
	}
	return userID, nil
}

func (r Resolver) ResolveDelegated(source, subject string) (string, error) {
	if r.NamespaceID == "" || source == "" || subject == "" || len(r.Key) != 32 {
		return "", ErrIdentityMissing
	}
	mac := hmac.New(sha256.New, r.Key)
	_, _ = mac.Write([]byte(r.NamespaceID + ":" + source + ":" + subject))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return "usr_" + strings.ToLower(encoded[:26]), nil
}
