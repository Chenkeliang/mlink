package identity

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDelegatedIdentityUsesKeyedStableHMAC(t *testing.T) {
	r := Resolver{NamespaceID: "ns-a", Key: bytes.Repeat([]byte{0x2a}, 32)}
	a, err := r.ResolveDelegated("feishu", "ou_stable")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.ResolveDelegated("feishu", "ou_stable")
	c, _ := r.ResolveDelegated("feishu", "ou_other")
	if a != b {
		t.Fatalf("unstable IDs: %q != %q", a, b)
	}
	if a == c || !strings.HasPrefix(a, "usr_") || len(a) != 30 {
		t.Fatalf("bad IDs: %q %q", a, c)
	}
}

func TestDelegatedIdentityRejectsInvalidInputs(t *testing.T) {
	tests := []Resolver{
		{NamespaceID: "", Key: make([]byte, 32)},
		{NamespaceID: "ns", Key: make([]byte, 31)},
	}
	for _, resolver := range tests {
		if _, err := resolver.ResolveDelegated("feishu", "ou_stable"); !errors.Is(err, ErrIdentityMissing) {
			t.Fatalf("ResolveDelegated() error = %v", err)
		}
	}
	resolver := Resolver{NamespaceID: "ns", Key: make([]byte, 32)}
	for _, input := range [][2]string{{"", "ou_stable"}, {"feishu", ""}} {
		if _, err := resolver.ResolveDelegated(input[0], input[1]); !errors.Is(err, ErrIdentityMissing) {
			t.Fatalf("ResolveDelegated(%q, %q) error = %v", input[0], input[1], err)
		}
	}
}

func TestFixedIdentityRequiresConfiguredUser(t *testing.T) {
	resolver := Resolver{NamespaceID: "ns", Key: make([]byte, 32)}
	got, err := resolver.ResolveFixed("usr_personal")
	if err != nil {
		t.Fatal(err)
	}
	if got != "usr_personal" {
		t.Fatalf("ResolveFixed() = %q", got)
	}
	if _, err := resolver.ResolveFixed(""); !errors.Is(err, ErrIdentityMissing) {
		t.Fatalf("ResolveFixed() error = %v", err)
	}
}
