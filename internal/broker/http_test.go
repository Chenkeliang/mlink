package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/identity"
	"mlink/internal/model"
	"mlink/internal/provider/host"
)

type recallRecordingProvider struct {
	request model.RecallRequest
	route   connection.RouteKey
	calls   int
}

func (p *recallRecordingProvider) CaptureTurn(context.Context, connection.RouteKey, host.CallMeta, model.Turn) (model.WriteReceipt, error) {
	return model.WriteReceipt{}, nil
}

func (p *recallRecordingProvider) Recall(_ context.Context, _ connection.RouteKey, _ host.CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	p.calls++
	p.request = request
	return model.ContextBundle{Items: []model.ContextItem{{ID: "memory-1", Text: "remembered"}}}, nil
}

func testRoutedHermesServer(t *testing.T) (*httptest.Server, *recallRecordingProvider) {
	t.Helper()
	provider := &recallRecordingProvider{}
	token := "hermes-token"
	digest := sha256.Sum256([]byte(token))
	router := identity.Router{
		NamespaceID: "personal", Key: bytes.Repeat([]byte{0x2a}, 32),
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2"}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC},
		},
		Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Bindings: identity.BindingSet{Values: []identity.BindingValue{{
			RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_owner"), PrincipalID: "owner",
		}}},
		Hermes: config.HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"},
	}
	server := httptest.NewServer((Server{
		Service: Service{Provider: provider},
		Authorizer: Authorizer{Router: &router, Grants: []Grant{{
			TokenDigest: digest[:], AdapterID: "hermes", Mode: IdentityDelegated, Source: "feishu",
			AllowedSpaceIDs: map[string]bool{"personal-owner": true, "hermes-private": true, "hermes-groups": true},
		}}},
	}).Handler(false))
	t.Cleanup(server.Close)
	return server, provider
}

func TestHermesHTTPRoutesOwnerOtherAndGroupWithoutForwardingRawIDs(t *testing.T) {
	server, provider := testRoutedHermesServer(t)
	cases := []struct {
		name, wantAgentID, wantUserPrefix string
		wantShared                        bool
		input                             map[string]any
	}{
		{"owner", "keliang-personal", "usr_owner_", true, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "dm", "chat_id": "oc_dm", "alternate_subject": "on_owner", "query": "x"}},
		{"other", "hermes-private", "usr_", false, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "dm", "chat_id": "oc_dm2", "alternate_subject": "on_other", "query": "x"}},
		{"group", "hermes-groups", "grp_", false, map[string]any{"adapter_id": "hermes", "source": "feishu", "chat_type": "group", "chat_id": "oc_group", "thread_id": "omt_topic", "alternate_subject": "on_other", "query": "x"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := postBrokerJSON(t, server.URL+"/v1/recall", "hermes-token", test.input)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			got := provider.request
			if got.Identity.AgentID != test.wantAgentID || !strings.HasPrefix(got.Identity.UserID, test.wantUserPrefix) || got.IncludeAgentShared != test.wantShared {
				t.Fatalf("request = %#v", got)
			}
			if strings.Contains(got.Identity.UserID, "on_") || strings.Contains(got.Identity.UserID, "oc_") {
				t.Fatal("raw external ID reached Provider")
			}
		})
	}
}

func TestHermesGroupMissingChatIDFailsWithoutProviderCall(t *testing.T) {
	server, provider := testRoutedHermesServer(t)
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
