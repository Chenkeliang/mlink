package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/identity"
	"mlink/internal/journal"
	"mlink/internal/model"
)

func TestExternalIdentityDoesNotLeakIntoCanonicalArtifacts(t *testing.T) {
	const rawAlias = "on_external_secret"
	const rawChat = "oc_group_secret"
	const rawThread = "omt_topic_secret"
	configuration, key := routingFixture()
	router := routerFromFixture(configuration, key, identity.BindingSet{})
	resolved, err := router.ResolveHermes(identity.ExternalContext{
		Source: "feishu", ChatType: "group", ChatID: rawChat, ThreadID: rawThread, AlternateSubject: rawAlias,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawAlias, rawChat, rawThread} {
		if bytes.Contains(canonical, []byte(raw)) {
			t.Fatalf("canonical identity leaked %q: %s", raw, canonical)
		}
	}

	path := filepath.Join(t.TempDir(), "journal.db")
	store, err := journal.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.EnqueueTurn(context.Background(), journal.Envelope{
		AdapterID: "hermes",
		Route:     connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2"},
		Turn: model.Turn{
			Identity:    model.IdentityScope{ConnectionID: "local", TenantID: resolved.TenantID, AgentID: resolved.AgentID, UserID: resolved.UserID, SessionID: resolved.SessionID, TurnID: identity.CanonicalTurnID(key, resolved.ActorDigest, "incoming")},
			ActorDigest: resolved.ActorDigest,
			Messages:    []model.Message{{Role: "user", Content: "benign group content", OccurredAt: time.Now().UTC()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawAlias, rawChat, rawThread} {
		if bytes.Contains(database, []byte(raw)) {
			t.Fatalf("Journal leaked %q", raw)
		}
	}
}
