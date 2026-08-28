package connection

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewSnapshotBuildsImmutableRoute(t *testing.T) {
	config := json.RawMessage(`{"base_url":"http://127.0.0.1:8420"}`)
	secretRefs := map[string]string{"token": "keychain://memorycore/token"}
	snapshot, err := NewSnapshot(
		"connection-a",
		ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"},
		"revision-1",
		config,
		secretRefs,
	)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	config[2] = 'X'
	secretRefs["token"] = "changed"
	if got := string(snapshot.Config()); got != `{"base_url":"http://127.0.0.1:8420"}` {
		t.Fatalf("Config() = %s, input mutation changed snapshot", got)
	}
	if got := snapshot.SecretRefs()["token"]; got != "keychain://memorycore/token" {
		t.Fatalf("SecretRefs()[token] = %q, input mutation changed snapshot", got)
	}

	returnedConfig := snapshot.Config()
	returnedConfig[2] = 'Y'
	returnedRefs := snapshot.SecretRefs()
	returnedRefs["token"] = "changed-again"
	if got := string(snapshot.Config()); got != `{"base_url":"http://127.0.0.1:8420"}` {
		t.Fatalf("Config() = %s, returned value mutation changed snapshot", got)
	}
	if got := snapshot.SecretRefs()["token"]; got != "keychain://memorycore/token" {
		t.Fatalf("SecretRefs()[token] = %q, returned map mutation changed snapshot", got)
	}

	want := RouteKey{
		ConnectionID:    "connection-a",
		ProviderID:      "dev.mlink.tencentdb",
		ProviderVersion: "0.1.0",
		ConfigRevision:  "revision-1",
	}
	if got := snapshot.RouteKey(); got != want {
		t.Fatalf("RouteKey() = %#v, want %#v", got, want)
	}
}

func TestNewSnapshotRejectsInvalidRoute(t *testing.T) {
	validProvider := ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"}
	tests := []struct {
		name       string
		connection string
		provider   ProviderRef
		revision   string
	}{
		{name: "empty connection", provider: validProvider, revision: "revision-1"},
		{name: "connection whitespace", connection: " connection-a", provider: validProvider, revision: "revision-1"},
		{name: "connection punctuation", connection: "connection/a", provider: validProvider, revision: "revision-1"},
		{name: "connection too long", connection: strings.Repeat("a", 129), provider: validProvider, revision: "revision-1"},
		{name: "empty provider", connection: "connection-a", provider: ProviderRef{Version: "0.1.0"}, revision: "revision-1"},
		{name: "provider not reverse domain", connection: "connection-a", provider: ProviderRef{ID: "tencentdb", Version: "0.1.0"}, revision: "revision-1"},
		{name: "provider invalid label", connection: "connection-a", provider: ProviderRef{ID: "dev.-mlink.tencentdb", Version: "0.1.0"}, revision: "revision-1"},
		{name: "version missing patch", connection: "connection-a", provider: ProviderRef{ID: "dev.mlink.tencentdb", Version: "1.0"}, revision: "revision-1"},
		{name: "version leading zero", connection: "connection-a", provider: ProviderRef{ID: "dev.mlink.tencentdb", Version: "01.0.0"}, revision: "revision-1"},
		{name: "version with prefix", connection: "connection-a", provider: ProviderRef{ID: "dev.mlink.tencentdb", Version: "v1.0.0"}, revision: "revision-1"},
		{name: "empty revision", connection: "connection-a", provider: validProvider},
		{name: "revision unicode", connection: "connection-a", provider: validProvider, revision: "版本-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSnapshot(tt.connection, tt.provider, tt.revision, nil, nil)
			if err == nil {
				t.Fatal("NewSnapshot() error = nil, want validation error")
			}
		})
	}
}

func TestRouteKeyExcludesRequestIdentity(t *testing.T) {
	snapshot, err := NewSnapshot(
		"connection-a",
		ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"},
		"revision-1",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	raw, err := json.Marshal(snapshot.RouteKey())
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{"tenant", "user", "agent", "session", "turn"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("RouteKey JSON = %s, contains request identity field %q", raw, forbidden)
		}
	}
}
