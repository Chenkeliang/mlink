package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"mlink/internal/broker"
	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/journal"
)

type mappedAgents struct {
	calls []controlplane.PrincipalIntent
}

func (mapped *mappedAgents) ResolveOrCreate(_ context.Context, intent controlplane.PrincipalIntent) (journal.PrincipalAgent, error) {
	mapped.calls = append(mapped.calls, intent)
	suffix := intent.Fingerprint[len(intent.Fingerprint)-8:]
	return journal.PrincipalAgent{
		Fingerprint: intent.Fingerprint, RouteKind: intent.RouteKind, BackendUserID: "usr-owner-generated",
		BackendTeamID: "team-owner-generated", BackendAgentID: "agt-" + suffix,
		BackendAssetID: "chat_memory-team-owner-generated-agt-" + suffix, DisplayLabel: intent.DisplayLabel, State: "active",
	}, nil
}

func TestControlPlaneRoutingMatrixUsesGeneratedIDsOnly(t *testing.T) {
	router := identity.Router{
		NamespaceID: "installation-1", Key: bytes.Repeat([]byte{0x2a}, 32),
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-control-plane-1"}},
		Principals:  map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner-generated", Kind: config.PrincipalPerson}},
		Bindings:    identity.BindingSet{Values: []identity.BindingValue{{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_owner_raw"), PrincipalID: "owner"}}},
	}
	mapped := &mappedAgents{}
	authorizer := broker.Authorizer{
		Router: &router, ConnectionID: "local", DynamicAgents: mapped,
		ControlPlane: &config.ControlPlane{ProviderID: "dev.mlink.tencentdb", InstanceID: "default", OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated", OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated"},
	}
	grant := broker.Grant{AdapterID: "hermes", Mode: broker.IdentityDelegated, Source: "feishu", AllowedSpaceIDs: map[string]bool{"owner": true, "hermes-private": true, "hermes-groups": true}}
	contexts := []identity.ExternalContext{
		{Source: "feishu", ChatType: "dm", ChatID: "oc_owner_raw", AlternateSubject: "on_owner_raw"},
		{Source: "feishu", ChatType: "dm", ChatID: "oc_dm_a_raw", AlternateSubject: "on_dm_a_raw"},
		{Source: "feishu", ChatType: "dm", ChatID: "oc_dm_b_raw", AlternateSubject: "on_dm_b_raw"},
		{Source: "feishu", ChatType: "group", ChatID: "oc_group_a_raw", ThreadID: "omt_topic_1_raw", AlternateSubject: "on_actor_raw"},
		{Source: "feishu", ChatType: "group", ChatID: "oc_group_b_raw", ThreadID: "omt_topic_1_raw", AlternateSubject: "on_actor_raw"},
		{Source: "feishu", ChatType: "group", ChatID: "oc_group_a_raw", ThreadID: "omt_topic_2_raw", AlternateSubject: "on_actor_raw"},
	}
	results := make([]broker.Authorization, 0, len(contexts))
	for _, external := range contexts {
		resolved, err := authorizer.Resolve(context.Background(), grant, external, "", "")
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, resolved)
		if resolved.Route != (connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-control-plane-1"}) {
			t.Fatalf("route = %#v", resolved.Route)
		}
		rendered := fmt.Sprintf("%#v", resolved)
		for _, raw := range []string{"oc_", "on_", "omt_"} {
			if strings.Contains(rendered, raw) {
				t.Fatalf("authorization leaked %q: %s", raw, rendered)
			}
		}
	}
	if results[0].Identity.AgentID != "agt-owner-generated" || !results[0].IncludeAgentShared {
		t.Fatalf("Owner = %#v", results[0])
	}
	if results[1].Identity.AgentID == results[2].Identity.AgentID || results[1].IncludeAgentShared || results[2].IncludeAgentShared {
		t.Fatalf("DM isolation = %#v/%#v", results[1], results[2])
	}
	if results[3].Identity.AgentID == results[4].Identity.AgentID || results[3].Identity.AgentID != results[5].Identity.AgentID ||
		results[3].Identity.SessionID == results[5].Identity.SessionID || results[3].IncludeAgentShared || results[5].IncludeAgentShared {
		t.Fatalf("group/topic isolation = %#v/%#v/%#v", results[3], results[4], results[5])
	}
}
