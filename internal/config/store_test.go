package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreRoundTripDoesNotContainSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := Store{Path: path}
	want := Config{
		SchemaVersion:      1,
		NamespaceID:        "personal",
		ActiveConnectionID: "local",
		Connections: map[string]Connection{
			"local": {
				ID:              "local",
				ProviderID:      "dev.mlink.tencentdb",
				ProviderVersion: "0.1.0",
				ConfigRevision:  "rev-1",
				ProviderConfig:  map[string]any{"endpoint": "http://127.0.0.1:8096"},
				SecretRefs: map[string]string{
					"token": "keychain://dev.mlink/connection/local/token",
				},
				TenantID:           "personal",
				AgentID:            "default",
				UserID:             "user-a",
				IncludeAgentShared: true,
			},
		},
		Adapters: map[string]Adapter{
			"codex": {ID: "codex", Enabled: true, ConnectionID: "local"},
		},
	}
	if err := store.SaveAtomic(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("actual-secret")) {
		t.Fatal("secret leaked")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func TestStoreLoadMissingConfig(t *testing.T) {
	_, err := (Store{Path: filepath.Join(t.TempDir(), "missing.yaml")}).Load()
	if !os.IsNotExist(err) {
		t.Fatalf("Load() error = %v, want os.ErrNotExist", err)
	}
}
