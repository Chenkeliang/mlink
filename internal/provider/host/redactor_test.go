package host

import (
	"strings"
	"testing"
)

func TestRedactingBufferRedactsSecretsSplitAcrossWrites(t *testing.T) {
	const secret = "token-SPLIT-秘密-value"
	buffer := newRedactingBuffer([]string{secret, "short-secret"}, 1024)
	chunks := []string{"before token-SP", "LIT-秘", "密-value after short-", "secret done"}
	for _, chunk := range chunks {
		if _, err := buffer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	got := buffer.String()
	if strings.Contains(got, secret) || strings.Contains(got, "short-secret") {
		t.Fatalf("redacted output leaked a secret: %q", got)
	}
	if !strings.Contains(got, "before [REDACTED] after [REDACTED] done") {
		t.Fatalf("redacted output = %q", got)
	}
}

func TestRedactingBufferKeepsOnlyTailCapacity(t *testing.T) {
	buffer := newRedactingBuffer(nil, 16)
	_, _ = buffer.Write([]byte("0123456789abcdefghijklmnop"))
	if got := buffer.String(); got != "abcdefghijklmnop" {
		t.Fatalf("String() = %q, want final 16 bytes", got)
	}
}

func TestJSONContainsSecretAfterUnescaping(t *testing.T) {
	secret := "token/秘密"
	raw := []byte(`{"nested":{"value":"token\/\u79d8\u5bc6"},"safe":"ok"}`)
	found, err := jsonContainsSecret(raw, []string{secret})
	if err != nil {
		t.Fatalf("jsonContainsSecret() error = %v", err)
	}
	if !found {
		t.Fatal("jsonContainsSecret() = false, want escaped secret detection")
	}
	found, err = jsonContainsSecret([]byte(`{"value":"safe"}`), []string{secret})
	if err != nil || found {
		t.Fatalf("safe json result = %v/%v", found, err)
	}
}

func TestJSONContainsSecretRejectsInvalidJSON(t *testing.T) {
	if _, err := jsonContainsSecret([]byte(`{"value":`), []string{"secret"}); err == nil {
		t.Fatal("jsonContainsSecret() error = nil, want invalid JSON error")
	}
}
