package identity

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"mlink/internal/config"
)

func TestOldAndNewAliasesResolveToSameOwner(t *testing.T) {
	router := fixtureRouter(t, []BindingValue{
		{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_old"), PrincipalID: "owner"},
		{RefID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", Value: []byte("on_new"), PrincipalID: "owner"},
	})
	old, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_old"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_new"})
	if err != nil {
		t.Fatal(err)
	}
	if old.UserID != "usr_owner_keliang" || newer.UserID != old.UserID || old.SpaceID != "personal-owner" || !old.IncludeAgentShared {
		t.Fatalf("resolved = %#v %#v", old, newer)
	}
}

func TestRouterEmitsOwnerIntentWithoutDynamicFingerprint(t *testing.T) {
	router := fixtureRouter(t, []BindingValue{
		{RefID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", Value: []byte("on_old"), PrincipalID: "owner"},
		{RefID: "owner-feishu-union-2", Source: "feishu", Kind: "union_id", Value: []byte("on_new"), PrincipalID: "owner"},
	})
	old, err := router.ResolveHermesIntent(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_old"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := router.ResolveHermesIntent(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_new"})
	if err != nil {
		t.Fatal(err)
	}
	if old.Kind != RouteOwner || newer.Kind != RouteOwner || old.PrincipalFingerprint != "" || newer.PrincipalFingerprint != "" || !old.IncludeAgentShared || !newer.IncludeAgentShared {
		t.Fatalf("owner intents = %#v %#v", old, newer)
	}
}

func TestRouterEmitsStablePrivateFingerprintWithoutRawIdentity(t *testing.T) {
	router := fixtureRouter(t, nil)
	context := ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_private_raw", AlternateSubject: "on_private_raw"}
	first, err := router.ResolveHermesIntent(context)
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.ResolveHermesIntent(context)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != RoutePrivate || first.PrincipalFingerprint == "" || first.PrincipalFingerprint != second.PrincipalFingerprint || first.IncludeAgentShared {
		t.Fatalf("private intents = %#v %#v", first, second)
	}
	rendered := fmt.Sprintf("%#v", first)
	for _, raw := range []string{context.ChatID, context.AlternateSubject} {
		if strings.Contains(rendered, raw) {
			t.Fatalf("intent leaked raw identity %q: %s", raw, rendered)
		}
	}
}

func TestRouterEmitsGroupFingerprintAndTopicScopedSession(t *testing.T) {
	router := fixtureRouter(t, nil)
	base := ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group_raw", ThreadID: "omt_topic_one", AlternateSubject: "on_actor_raw"}
	first, err := router.ResolveHermesIntent(base)
	if err != nil {
		t.Fatal(err)
	}
	otherActor := base
	otherActor.AlternateSubject = "on_other_raw"
	second, _ := router.ResolveHermesIntent(otherActor)
	otherTopic := base
	otherTopic.ThreadID = "omt_topic_two"
	third, _ := router.ResolveHermesIntent(otherTopic)
	if first.Kind != RouteGroup || first.PrincipalFingerprint != second.PrincipalFingerprint || first.PrincipalFingerprint != third.PrincipalFingerprint {
		t.Fatalf("group fingerprints = %#v %#v %#v", first, second, third)
	}
	if first.SessionID != second.SessionID || first.SessionID == third.SessionID || first.ActorDigest == second.ActorDigest || first.IncludeAgentShared {
		t.Fatalf("group sessions = %#v %#v %#v", first, second, third)
	}
	rendered := fmt.Sprintf("%#v", first)
	for _, raw := range []string{base.ChatID, base.ThreadID, base.AlternateSubject} {
		if strings.Contains(rendered, raw) {
			t.Fatalf("intent leaked raw identity %q: %s", raw, rendered)
		}
	}
}

func TestGroupMembersSharePrincipalAndTopicSession(t *testing.T) {
	router := fixtureRouter(t, nil)
	a, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_topic", AlternateSubject: "on_a"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_topic", AlternateSubject: "on_b"})
	other, _ := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_other", AlternateSubject: "on_b"})
	if a.UserID != b.UserID || a.SessionID != b.SessionID {
		t.Fatalf("same topic split: %#v %#v", a, b)
	}
	if a.UserID != other.UserID || a.SessionID == other.SessionID {
		t.Fatalf("topic scope = %#v %#v", a, other)
	}
	if a.IncludeAgentShared || a.ActorDigest == b.ActorDigest || a.AgentID != "hermes-groups" {
		t.Fatalf("group identities = %#v %#v", a, b)
	}
}

func TestRevokedAliasFailsClosedAndUnknownAliasUsesPrivateSpace(t *testing.T) {
	router := fixtureRouter(t, []BindingValue{{RefID: "old", Source: "feishu", Kind: "union_id", Value: []byte("on_old"), PrincipalID: "owner", Revoked: true}})
	_, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_old"})
	if !errors.Is(err, ErrBindingRevoked) {
		t.Fatalf("revoked alias error = %v", err)
	}
	got, err := router.ResolveHermes(ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_never_bound"})
	if err != nil || got.SpaceID != "hermes-private" || got.IncludeAgentShared || got.UserID == "usr_owner_keliang" {
		t.Fatalf("unknown alias = %#v %v", got, err)
	}
}

func TestFixedCodexAndPiNeverNeedFeishuIdentity(t *testing.T) {
	router := fixtureRouter(t, nil)
	for _, adapter := range []string{"codex", "pi"} {
		got, err := router.ResolveFixed(adapter, "native-session")
		if err != nil || got.UserID != "usr_owner_keliang" || got.SessionID != "native-session" || got.SpaceID != "personal-owner" {
			t.Fatalf("%s = %#v %v", adapter, got, err)
		}
	}
}

func TestHermesRouterFailsClosedOnMissingOrUnsupportedContext(t *testing.T) {
	router := fixtureRouter(t, nil)
	tests := []struct {
		context ExternalContext
		want    error
	}{
		{ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm"}, ErrIdentityMissing},
		{ExternalContext{Source: "feishu", ChatType: "group", AlternateSubject: "on_a"}, ErrGroupIdentityMissing},
		{ExternalContext{Source: "discord", ChatType: "dm", ChatID: "c", AlternateSubject: "u"}, ErrUnsupportedContext},
		{ExternalContext{Source: "feishu", ChatType: "channel", ChatID: "c", AlternateSubject: "u"}, ErrUnsupportedContext},
	}
	for _, test := range tests {
		if _, err := router.ResolveHermes(test.context); !errors.Is(err, test.want) {
			t.Fatalf("ResolveHermes(%#v) error = %v, want %v", test.context, err, test.want)
		}
	}
}

func TestCanonicalTurnIDSeparatesActorsAndIsStable(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, 32)
	a := CanonicalTurnID(key, "actor_a", "turn_same")
	b := CanonicalTurnID(key, "actor_b", "turn_same")
	if a == "" || a == b || a != CanonicalTurnID(key, "actor_a", "turn_same") {
		t.Fatalf("turn IDs = %q %q", a, b)
	}
}

func fixtureRouter(t *testing.T, bindings []BindingValue) Router {
	t.Helper()
	return Router{
		NamespaceID: "personal",
		Key:         bytes.Repeat([]byte{0x2a}, 32),
		Connections: map[string]config.Connection{"local": {
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2",
		}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC},
		},
		Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Adapters: map[string]config.Adapter{
			"codex": {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
			"pi":    {ID: "pi", Enabled: true, SpaceID: "personal-owner"},
		},
		Bindings: BindingSet{Values: bindings},
		Hermes:   config.HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"},
	}
}
