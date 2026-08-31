//go:build integration

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"mlink/internal/broker"
	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/identity"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/tencentdb"
)

var liveRunIDPattern = regexp.MustCompile(`^mlink-alias-[A-Za-z0-9._-]{1,96}$`)

func TestLiveAliasRotationWithoutMemoryMigration(t *testing.T) {
	provider, configuration, key, runID := liveRoutingFixture(t)
	oldBinding := identity.BindingValue{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_old_" + runID), PrincipalID: "owner"}
	router := liveRouter(configuration, key, []identity.BindingValue{oldBinding})
	oldOwner, err := router.ResolveHermes(liveDM("on_old_"+runID, "old"))
	if err != nil {
		t.Fatal(err)
	}
	canary := "MLINK_ALIAS_CANARY_" + fmt.Sprint(time.Now().UnixNano())
	captureLiveCanary(t, provider, oldOwner, "turn-owner", canary)
	waitForLiveCanary(t, provider, oldOwner, canary, true)

	codex, err := router.ResolveFixed("codex", "codex-session")
	if err != nil {
		t.Fatal(err)
	}
	pi, err := router.ResolveFixed("pi", "pi-session")
	if err != nil {
		t.Fatal(err)
	}
	waitForLiveCanary(t, provider, codex, canary, true)
	waitForLiveCanary(t, provider, pi, canary, true)

	newBinding := identity.BindingValue{RefID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", Value: []byte("on_new_" + runID), PrincipalID: "owner"}
	rotated := liveRouter(configuration, key, []identity.BindingValue{oldBinding, newBinding})
	newOwner, err := rotated.ResolveHermes(liveDM("on_new_"+runID, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if newOwner.UserID != oldOwner.UserID || newOwner.SpaceID != oldOwner.SpaceID {
		t.Fatalf("alias changed canonical owner")
	}
	waitForLiveCanary(t, provider, newOwner, canary, true)
	brokerToken := "broker-" + runID
	firstBroker := newLiveBrokerServer(t, rotated, provider, brokerToken)
	assertLiveBrokerRecall(t, firstBroker.URL, brokerToken, "on_new_"+runID, canary)
	firstBroker.Close()

	bundle := identity.BundleV1{
		SchemaVersion: 1, Principals: configuration.Principals, Spaces: configuration.Spaces,
		IdentityKey: append([]byte(nil), key...),
		Bindings: []identity.BindingExport{
			{Ref: configuration.Bindings["owner-feishu-union-1"], Value: append([]byte(nil), oldBinding.Value...)},
			{Ref: configuration.Bindings["owner-feishu-union-2"], Value: append([]byte(nil), newBinding.Value...)},
		},
		CreatedAt: time.Now().UTC(),
	}
	encrypted, err := identity.EncryptBundle(bundle, []byte("passphrase-12"), bytes.NewReader(bytes.Repeat([]byte{0x51}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := identity.DecryptBundle(encrypted, []byte("passphrase-12"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Wipe()
	reloadedValues := make([]identity.BindingValue, 0, len(restored.Bindings))
	for _, binding := range restored.Bindings {
		reloadedValues = append(reloadedValues, identity.BindingValue{RefID: binding.Ref.ID, Source: binding.Ref.Source, Kind: binding.Ref.Kind, Value: append([]byte(nil), binding.Value...), PrincipalID: binding.Ref.PrincipalID, Revoked: binding.Ref.Status == config.BindingRevoked})
	}
	reloaded := liveRouter(configuration, restored.IdentityKey, reloadedValues)
	reloadedOwner, err := reloaded.ResolveHermes(liveDM("on_new_"+runID, "reload"))
	if err != nil || reloadedOwner.UserID != oldOwner.UserID {
		t.Fatalf("restored owner changed")
	}
	waitForLiveCanary(t, provider, reloadedOwner, canary, true)
	restartedBroker := newLiveBrokerServer(t, reloaded, provider, brokerToken)
	assertLiveBrokerRecall(t, restartedBroker.URL, brokerToken, "on_new_"+runID, canary)
	restartedBroker.Close()

	revokedOld := oldBinding
	revokedOld.Revoked = true
	revoked := liveRouter(configuration, key, []identity.BindingValue{revokedOld, newBinding})
	if _, err := revoked.ResolveHermes(liveDM("on_old_"+runID, "revoked")); !errors.Is(err, identity.ErrBindingRevoked) {
		t.Fatalf("old alias after revoke = %v", err)
	}
	activeOwner, err := revoked.ResolveHermes(liveDM("on_new_"+runID, "active"))
	if err != nil {
		t.Fatal(err)
	}
	waitForLiveCanary(t, provider, activeOwner, canary, true)

	unbound, err := revoked.ResolveHermes(liveDM("on_other_"+runID, "other"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := revoked.ResolveHermes(liveGroup("oc_group_"+runID, "omt_topic_a", "on_other_"+runID))
	if err != nil {
		t.Fatal(err)
	}
	waitForLiveCanary(t, provider, unbound, canary, false)
	waitForLiveCanary(t, provider, group, canary, false)

	derivedOld := liveRouter(configuration, key, nil)
	oldAsUnbound, err := derivedOld.ResolveHermes(liveDM("on_old_"+runID, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	waitForLiveCanary(t, provider, oldAsUnbound, canary, false)
	t.Logf("alias continuity passed for isolated run %s", runID)
}

func TestLiveGroupTopicsShareGroupMemoryAndRemainSeparatedFromOwner(t *testing.T) {
	provider, configuration, key, runID := liveRoutingFixture(t)
	router := liveRouter(configuration, key, []identity.BindingValue{{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_owner_" + runID), PrincipalID: "owner"}})
	topicA, err := router.ResolveHermes(liveGroup("oc_group_a_"+runID, "omt_topic_a", "on_a_"+runID))
	if err != nil {
		t.Fatal(err)
	}
	canary := "MLINK_GROUP_CANARY_" + fmt.Sprint(time.Now().UnixNano())
	captureLiveCanary(t, provider, topicA, "turn-group", canary)
	waitForLiveCanary(t, provider, topicA, canary, true)
	topicB, _ := router.ResolveHermes(liveGroup("oc_group_a_"+runID, "omt_topic_b", "on_b_"+runID))
	if topicA.UserID != topicB.UserID || topicA.SessionID == topicB.SessionID {
		t.Fatalf("group topic scope differs")
	}
	waitForLiveCanary(t, provider, topicB, canary, true)
	otherGroup, _ := router.ResolveHermes(liveGroup("oc_group_b_"+runID, "omt_topic_a", "on_a_"+runID))
	owner, _ := router.ResolveHermes(liveDM("on_owner_"+runID, "owner"))
	unbound, _ := router.ResolveHermes(liveDM("on_other_"+runID, "other"))
	waitForLiveCanary(t, provider, otherGroup, canary, false)
	waitForLiveCanary(t, provider, owner, canary, false)
	waitForLiveCanary(t, provider, unbound, canary, false)
	if topicA.IncludeAgentShared || topicB.IncludeAgentShared || unbound.IncludeAgentShared || !owner.IncludeAgentShared {
		t.Fatal("Space shared-layer policy differs from contract")
	}
	t.Logf("group separation passed for isolated run %s", runID)
}

func liveRoutingFixture(t *testing.T) (*tencentdb.Provider, config.Config, []byte, string) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_URL"))
	token := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_TOKEN"))
	runID := strings.TrimSpace(os.Getenv("MLINK_TEST_RUN_ID"))
	if baseURL != "http://127.0.0.1:8420" || token == "" || !liveRunIDPattern.MatchString(runID) {
		t.Fatalf("isolated live configuration is missing or unsafe")
	}
	client, err := tencentdb.NewClient(tencentdb.Config{BaseURL: baseURL, Token: token, ServiceID: runID, HTTPClient: &http.Client{Timeout: 8 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	ownerAgent := "owner-" + runID
	privateAgent := "private-" + runID
	groupAgent := "groups-" + runID
	configuration := config.Config{
		SchemaVersion: 2, NamespaceID: "team-" + runID, ActiveConnectionID: "local",
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-" + runID}},
		Principals:  map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner-" + runID, Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "team-" + runID, AgentID: ownerAgent, IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "team-" + runID, AgentID: privateAgent, PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "team-" + runID, AgentID: groupAgent, PrincipalPolicy: config.PolicyGroupHMAC},
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
	return tencentdb.NewProvider(client), configuration, bytes.Repeat([]byte{0x2a}, 32), runID
}

func liveRouter(configuration config.Config, key []byte, values []identity.BindingValue) identity.Router {
	return identity.Router{
		NamespaceID: configuration.NamespaceID, Key: append([]byte(nil), key...), Connections: configuration.Connections,
		Spaces: configuration.Spaces, Principals: configuration.Principals, Adapters: configuration.Adapters,
		Bindings: identity.BindingSet{Values: values}, Hermes: *configuration.Adapters["hermes"].HermesRouting,
	}
}

func liveDM(alias, suffix string) identity.ExternalContext {
	return identity.ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm_" + suffix, AlternateSubject: alias}
}

func liveGroup(chatID, threadID, actor string) identity.ExternalContext {
	return identity.ExternalContext{Source: "feishu", ChatType: "group", ChatID: chatID, ThreadID: threadID, AlternateSubject: actor}
}

func captureLiveCanary(t *testing.T, provider *tencentdb.Provider, resolved identity.ResolvedIdentity, turnSuffix, canary string) {
	t.Helper()
	messages := make([]model.Message, 0, 10)
	userContent := "我的长期个人代号是 " + canary + "，以后询问个人代号时请使用它。"
	assistantContent := "已记录你的长期个人代号 " + canary + "。"
	if strings.HasPrefix(resolved.AgentID, "groups-") {
		userContent = "本群长期约定的测试代号是 " + canary + "，以后在本群询问测试代号时请使用它。"
		assistantContent = "已记录本群长期约定的测试代号 " + canary + "。"
	}
	for range 5 {
		messages = append(messages,
			model.Message{Role: "user", Content: userContent, OccurredAt: time.Now().UTC()},
			model.Message{Role: "assistant", Content: assistantContent, OccurredAt: time.Now().UTC()},
		)
	}
	turn := model.Turn{Identity: model.IdentityScope{
		ConnectionID: resolved.ConnectionID, TenantID: resolved.TenantID, AgentID: resolved.AgentID, UserID: resolved.UserID,
		SessionID: resolved.SessionID, TurnID: "turn-" + turnSuffix + "-" + fmt.Sprint(time.Now().UnixNano()),
	}, Messages: messages}
	receipt, err := provider.CaptureTurn(context.Background(), turn)
	if err != nil || receipt.State != model.WriteAccepted || receipt.ReplaySafe {
		t.Fatalf("capture receipt state=%q replay_safe=%t error=%v", receipt.State, receipt.ReplaySafe, err)
	}
}

func waitForLiveCanary(t *testing.T, provider *tencentdb.Provider, resolved identity.ResolvedIdentity, canary string, want bool) {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for {
		bundle, err := provider.Recall(context.Background(), model.RecallRequest{
			Identity: model.IdentityScope{ConnectionID: resolved.ConnectionID, TenantID: resolved.TenantID, AgentID: resolved.AgentID, UserID: resolved.UserID},
			Query:    canary, MaxItems: 10, IncludeAgentShared: resolved.IncludeAgentShared,
		})
		if err != nil {
			t.Fatalf("recall failed: %v", err)
		}
		found := false
		for _, item := range bundle.Items {
			found = found || strings.Contains(item.Text, canary)
		}
		if want && found {
			return
		}
		if !want {
			if found {
				t.Fatal("isolated identity recalled foreign canary")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("canary did not become visible before deadline")
		}
		time.Sleep(2 * time.Second)
	}
}

type liveBrokerProvider struct{ provider *tencentdb.Provider }

func (provider liveBrokerProvider) CaptureTurn(ctx context.Context, _ connection.RouteKey, _ host.CallMeta, turn model.Turn) (model.WriteReceipt, error) {
	return provider.provider.CaptureTurn(ctx, turn)
}

func (provider liveBrokerProvider) Recall(ctx context.Context, _ connection.RouteKey, _ host.CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	return provider.provider.Recall(ctx, request)
}

func newLiveBrokerServer(t *testing.T, router identity.Router, provider *tencentdb.Provider, token string) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256([]byte(token))
	server := httptest.NewServer((broker.Server{
		Service: broker.Service{Provider: liveBrokerProvider{provider: provider}},
		Authorizer: broker.Authorizer{Router: &router, Grants: []broker.Grant{{
			TokenDigest: digest[:], AdapterID: "hermes", Mode: broker.IdentityDelegated, Source: "feishu",
			AllowedSpaceIDs: map[string]bool{"personal-owner": true, "hermes-private": true, "hermes-groups": true},
		}}},
	}).Handler(false))
	return server
}

func assertLiveBrokerRecall(t *testing.T, baseURL, token, alias, canary string) {
	t.Helper()
	input := broker.RecallInput{
		AdapterID: "hermes", Source: "feishu", ChatType: "dm", ChatID: "oc_dm_broker",
		AlternateSubject: alias, Query: canary, MaxItems: 10,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/recall", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Broker recall status = %d", response.StatusCode)
	}
	var bundle model.ContextBundle
	if err := json.NewDecoder(response.Body).Decode(&bundle); err != nil {
		t.Fatal(err)
	}
	for _, item := range bundle.Items {
		if strings.Contains(item.Text, canary) {
			return
		}
	}
	t.Fatal("Broker did not recall owner canary after reconstruction")
}
