package e2e

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"mlink/internal/config"
	"mlink/internal/identity"
)

func TestOwnerAliasesRemainStableAcrossInterleavingRestartAndRevoke(t *testing.T) {
	configuration, key := routingFixture()
	secretStore := secrets{
		"identity/binding/owner-feishu-union-1": []byte("on_old"),
		"identity/binding/owner-feishu-union-2": []byte("on_new"),
	}
	bindings, err := (identity.Repository{Secrets: secretStore, IdentityKey: key}).Load(context.Background(), configuration.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer bindings.Wipe()
	router := routerFromFixture(configuration, key, bindings)
	for index := 0; index < 100; index++ {
		alias := "on_old"
		if index%2 == 1 {
			alias = "on_new"
		}
		got, err := router.ResolveHermes(dmContext(alias))
		if err != nil || got.UserID != "usr_owner_keliang" || got.SpaceID != "personal-owner" || !got.IncludeAgentShared {
			t.Fatalf("iteration %d = %#v, %v", index, got, err)
		}
	}

	// Simulate Broker reconstruction from persisted config and Keychain.
	reloadedBindings, err := (identity.Repository{Secrets: secretStore, IdentityKey: key}).Load(context.Background(), configuration.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer reloadedBindings.Wipe()
	reloaded := routerFromFixture(configuration, key, reloadedBindings)
	got, err := reloaded.ResolveHermes(dmContext("on_new"))
	if err != nil || got.UserID != "usr_owner_keliang" {
		t.Fatalf("reloaded = %#v, %v", got, err)
	}

	old := configuration.Bindings["owner-feishu-union-1"]
	old.Status = config.BindingRevoked
	configuration.Bindings[old.ID] = old
	revokedBindings, err := (identity.Repository{Secrets: secretStore, IdentityKey: key}).Load(context.Background(), configuration.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer revokedBindings.Wipe()
	revokedRouter := routerFromFixture(configuration, key, revokedBindings)
	if _, err := revokedRouter.ResolveHermes(dmContext("on_old")); !errors.Is(err, identity.ErrBindingRevoked) {
		t.Fatalf("revoked error = %v", err)
	}
	got, err = revokedRouter.ResolveHermes(dmContext("on_new"))
	if err != nil || got.UserID != "usr_owner_keliang" {
		t.Fatalf("new alias after revoke = %#v, %v", got, err)
	}
}

func routingFixture() (config.Config, []byte) {
	configuration := config.Config{
		SchemaVersion: 2, NamespaceID: "personal", ActiveConnectionID: "local",
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2"}},
		Principals:  map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC},
		},
		Adapters: map[string]config.Adapter{
			"codex":  {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
			"pi":     {ID: "pi", Enabled: true, SpaceID: "personal-owner"},
			"hermes": {ID: "hermes", Enabled: true, HermesRouting: &config.HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}},
		},
		Bindings: map[string]config.BindingRef{
			"owner-feishu-union-1": {ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive},
			"owner-feishu-union-2": {ID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-2", Status: config.BindingActive},
		},
	}
	return configuration, bytes.Repeat([]byte{0x2a}, 32)
}

func routerFromFixture(configuration config.Config, key []byte, bindings identity.BindingSet) identity.Router {
	return identity.Router{
		NamespaceID: configuration.NamespaceID, Key: append([]byte(nil), key...), Connections: configuration.Connections,
		Spaces: configuration.Spaces, Principals: configuration.Principals, Adapters: configuration.Adapters,
		Bindings: bindings, Hermes: *configuration.Adapters["hermes"].HermesRouting,
	}
}

func dmContext(alias string) identity.ExternalContext {
	return identity.ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: alias}
}
