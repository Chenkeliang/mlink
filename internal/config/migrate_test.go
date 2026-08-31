package config

import (
	"errors"
	"testing"
)

func TestV1RequiresExplicitMigrationPreview(t *testing.T) {
	raw := []byte("schema_version: 1\nnamespace_id: personal\nactive_connection_id: local\nconnections:\n  local:\n    id: local\n    provider_id: dev.mlink.tencentdb\n    provider_version: 0.1.0\n    config_revision: rev-1\n    tenant_id: personal\n    agent_id: default\n    user_id: keliang\n")
	_, err := Decode(raw)
	if !errors.Is(err, ErrMigrationRequired) {
		t.Fatalf("Decode() error = %v", err)
	}
	preview, err := PreviewV1Migration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if preview.FromVersion != 1 || preview.ToVersion != 2 || preview.OwnerCanonicalUserID != "usr_owner_keliang" {
		t.Fatalf("preview = %#v", preview)
	}
}
