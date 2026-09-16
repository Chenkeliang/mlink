package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
)

type recallRecordingProvider struct {
	request model.RecallRequest
	route   connection.RouteKey
	calls   int
}

type dynamicAgentStub struct {
	calls []controlplane.PrincipalIntent
	err   error
}

func (stub *dynamicAgentStub) ResolveOrCreate(_ context.Context, intent controlplane.PrincipalIntent) (journal.PrincipalAgent, error) {
	stub.calls = append(stub.calls, intent)
	if stub.err != nil {
		return journal.PrincipalAgent{}, stub.err
	}
	agentID := "agt-private-generated"
	if intent.RouteKind == "hermes-group" {
		agentID = "agt-group-generated"
	}
	return journal.PrincipalAgent{
		Fingerprint: intent.Fingerprint, RouteKind: intent.RouteKind, BackendUserID: "usr-owner-generated",
		BackendTeamID: "team-owner-generated", BackendAgentID: agentID,
		BackendAssetID: "chat_memory-team-owner-generated-" + agentID, DisplayLabel: intent.DisplayLabel, State: "active",
	}, nil
}

func (p *recallRecordingProvider) CaptureTurn(context.Context, connection.RouteKey, host.CallMeta, model.Turn) (model.WriteReceipt, error) {
	return model.WriteReceipt{}, nil
}

func (p *recallRecordingProvider) Recall(_ context.Context, _ connection.RouteKey, _ host.CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	p.calls++
	p.request = request
	return model.ContextBundle{Items: []model.ContextItem{{ID: "memory-1", Text: "remembered"}}}, nil
}

func testRoutedHermesServer(t *testing.T) (*httptest.Server, *recallRecordingProvider, *dynamicAgentStub) {
	t.Helper()
	provider := &recallRecordingProvider{}
	provisioner := &dynamicAgentStub{}
	token := "hermes-token"
	digest := sha256.Sum256([]byte(token))
	router := identity.Router{
		NamespaceID: "personal", Key: bytes.Repeat([]byte{0x2a}, 32),
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3"}},
		Principals:  map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner-generated", Kind: config.PrincipalPerson}},
		Bindings: identity.BindingSet{Values: []identity.BindingValue{{
			RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_owner"), PrincipalID: "owner",
		}}},
	}
	server := httptest.NewServer((Server{
		Service: Service{Provider: provider},
		Authorizer: Authorizer{Router: &router, ConnectionID: "local", ControlPlane: &config.ControlPlane{
			ProviderID: "dev.mlink.tencentdb", InstanceID: "default", PanelURL: "http://127.0.0.1:8125",
			OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated",
			OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated",
		}, DynamicAgents: provisioner, Grants: []Grant{{
			TokenDigest: digest[:], AdapterID: "hermes", Mode: IdentityDelegated, Source: "feishu",
			AllowedSpaceIDs: map[string]bool{"owner": true, "hermes-private": true, "hermes-groups": true},
		}}},
	}).Handler(false))
	t.Cleanup(server.Close)
	return server, provider, provisioner
}

func TestHermesHTTPRoutesThroughDynamicAgentMappings(t *testing.T) {
	server, provider, provisioner := testRoutedHermesServer(t)
	cases := []struct {
		name, wantAgentID string
		wantShared        bool
		wantProvisioned   bool
		input             map[string]any
	}{
		{"owner", "agt-owner-generated", true, false, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "dm", "chat_id": "oc_dm", "alternate_subject": "on_owner", "query": "x"}},
		{"other", "agt-private-generated", false, true, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "dm", "chat_id": "oc_dm2", "alternate_subject": "on_other", "query": "x"}},
		{"group", "agt-group-generated", false, true, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "group", "chat_id": "oc_group", "thread_id": "omt_topic", "alternate_subject": "on_other", "query": "x"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := len(provisioner.calls)
			response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", test.input)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			got := provider.request
			if got.Identity.AgentID != test.wantAgentID || got.Identity.UserID != "usr-owner-generated" || got.Identity.TenantID != "team-owner-generated" || got.IncludeAgentShared != test.wantShared {
				t.Fatalf("request = %#v", got)
			}
			if (len(provisioner.calls) > before) != test.wantProvisioned {
				t.Fatalf("provisioner calls = %#v", provisioner.calls)
			}
			if strings.Contains(fmt.Sprintf("%#v", got), "on_") || strings.Contains(fmt.Sprintf("%#v", got), "oc_") {
				t.Fatalf("raw external ID reached Provider: %#v", got)
			}
		})
	}
}

func TestHermesDynamicAgentProvisionFailureSkipsProvider(t *testing.T) {
	server, provider, provisioner := testRoutedHermesServer(t)
	provisioner.err = errors.New("backend owner-key-secret unavailable")
	response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "chat_type": "dm", "chat_id": "oc_dm2", "alternate_subject": "on_other", "query": "x",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || provider.calls != 0 {
		t.Fatalf("status/provider calls = %d/%d", response.StatusCode, provider.calls)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), "owner-key-secret") || !strings.Contains(string(body), "identity_forbidden") {
		t.Fatalf("unsafe error body = %s", body)
	}
}

func TestHermesGroupMissingChatIDFailsWithoutProviderCall(t *testing.T) {
	server, provider, _ := testRoutedHermesServer(t)
	response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "chat_type": "group", "alternate_subject": "on_a", "query": "x",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || provider.calls != 0 {
		t.Fatalf("status/calls = %d/%d", response.StatusCode, provider.calls)
	}
}

func testHermesServer(t *testing.T) (*httptest.Server, *recallRecordingProvider) {
	t.Helper()
	provider := &recallRecordingProvider{}
	token := "hermes-secret-token"
	digest := sha256.Sum256([]byte(token))
	server := httptest.NewServer((Server{
		Service: Service{Provider: provider},
		Authorizer: Authorizer{
			Resolver: identity.Resolver{NamespaceID: "personal", Key: bytes.Repeat([]byte{0x2a}, 32)},
			Grants: []Grant{{
				TokenDigest: digest[:], AdapterID: "hermes", Mode: IdentityDelegated, Source: "feishu",
				Route:    connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1"},
				TenantID: "personal", AgentID: "hermes", IncludeAgentShared: true,
			}},
		},
	}).Handler(false))
	t.Cleanup(server.Close)
	return server, provider
}

func TestHermesGrantRejectsDirectCanonicalUserOverride(t *testing.T) {
	server, _ := testHermesServer(t)
	response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-secret-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "source_subject": "ou_a", "user_id": "usr_forged", "query": "x",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestHermesGrantDerivesCanonicalUserAndRejectsWrongToken(t *testing.T) {
	server, provider := testHermesServer(t)
	response := postBrokerJSON(t, server.URL+"/v1/recall", "wrong", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "source_subject": "ou_a", "query": "x",
	})
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", response.StatusCode)
	}
	response = postBrokerJSON(t, server.URL+"/v1/recall", "hermes-secret-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "source_subject": "ou_a", "query": "x",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if provider.request.Identity.UserID == "" || provider.request.Identity.UserID == "ou_a" {
		t.Fatalf("canonical user = %q", provider.request.Identity.UserID)
	}
	if provider.request.Identity.TenantID != "personal" || provider.request.Identity.AgentID != "hermes" {
		t.Fatalf("identity = %#v", provider.request.Identity)
	}
}

func TestHermesDelegatedUsersRemainIsolatedWhenInterleaved(t *testing.T) {
	server, provider := testHermesServer(t)
	users := map[string]string{}
	for index := 0; index < 100; index++ {
		subject := "ou_a"
		if index%2 == 1 {
			subject = "ou_b"
		}
		response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-secret-token", map[string]any{
			"adapter_id": "hermes", "source": "feishu", "source_subject": subject, "query": "canary",
		})
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("iteration %d status = %d", index, response.StatusCode)
		}
		canonical := provider.request.Identity.UserID
		if previous := users[subject]; previous != "" && previous != canonical {
			t.Fatalf("subject %s changed canonical user: %q -> %q", subject, previous, canonical)
		}
		users[subject] = canonical
	}
	if users["ou_a"] == "" || users["ou_b"] == "" || users["ou_a"] == users["ou_b"] {
		t.Fatalf("delegated users were not isolated: %#v", users)
	}
}

func TestHermesDelegatedGrantFailsClosedWithoutStableIdentity(t *testing.T) {
	server, _ := testHermesServer(t)
	response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-secret-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "query": "canary",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestBrokerRejectsUnknownJSONFields(t *testing.T) {
	server, _ := testHermesServer(t)
	response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-secret-token", map[string]any{
		"adapter_id": "hermes", "source": "feishu", "source_subject": "ou_a", "query": "x", "surprise": true,
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestSessionFlushPersistsResolvedFinalization(t *testing.T) {
	store := brokerTestStore(t)
	route := connection.RouteKey{
		ConnectionID: "local", ProviderID: "dev.mlink.tencentdb",
		ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
	}
	server := httptest.NewServer((Server{
		Service: Service{Journal: store},
		Authorizer: Authorizer{
			Resolver: identity.Resolver{NamespaceID: "personal", Key: bytes.Repeat([]byte{0x2a}, 32)},
			Grants: []Grant{{
				AdapterID: "codex", Mode: IdentityFixed, Route: route,
				TenantID: "personal", AgentID: "codex", UserID: "usr-codex",
			}},
		},
	}).Handler(true))
	defer server.Close()
	response := postBrokerJSON(t, server.URL+"/v1/sessions/session-final/flush", "", map[string]any{
		"adapter_id": "codex",
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	claimed, err := store.ClaimReadyFinalizations(context.Background(), time.Now().UTC(), 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %#v %v", claimed, err)
	}
	if claimed[0].Route != route || claimed[0].Identity.UserID != "usr-codex" || claimed[0].Identity.SessionID != "session-final" {
		t.Fatalf("finalization = %#v", claimed[0])
	}
}

func postBrokerJSON(t *testing.T, url, token string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
