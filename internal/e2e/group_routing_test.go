package e2e

import (
	"errors"
	"testing"

	"mlink/internal/identity"
)

func TestGroupAndTopicRoutingIsSharedByConversationNotActor(t *testing.T) {
	configuration, key := routingFixture()
	router := routerFromFixture(configuration, key, identity.BindingSet{})
	var expectedUser, expectedSession string
	for index := 0; index < 100; index++ {
		actor := "on_a"
		if index%2 == 1 {
			actor = "on_b"
		}
		got, err := router.ResolveHermes(groupContext("oc_group", "omt_topic", actor))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			expectedUser, expectedSession = got.UserID, got.SessionID
		}
		if got.UserID != expectedUser || got.SessionID != expectedSession || got.IncludeAgentShared || got.AgentID != "hermes-groups" {
			t.Fatalf("iteration %d = %#v", index, got)
		}
	}
	a, _ := router.ResolveHermes(groupContext("oc_group", "omt_a", "on_a"))
	b, _ := router.ResolveHermes(groupContext("oc_group", "omt_b", "on_b"))
	otherGroup, _ := router.ResolveHermes(groupContext("oc_other", "omt_a", "on_a"))
	main, _ := router.ResolveHermes(groupContext("oc_group", "", "on_a"))
	if a.UserID != b.UserID || a.SessionID == b.SessionID {
		t.Fatalf("topic identity = %#v %#v", a, b)
	}
	if a.UserID == otherGroup.UserID || a.SessionID == otherGroup.SessionID {
		t.Fatalf("group identity = %#v %#v", a, otherGroup)
	}
	if main.UserID != a.UserID || main.SessionID == a.SessionID {
		t.Fatalf("main/topic = %#v %#v", main, a)
	}
}

func TestExternalContextFailuresAndCardFallback(t *testing.T) {
	configuration, key := routingFixture()
	router := routerFromFixture(configuration, key, identity.BindingSet{})
	for _, test := range []struct {
		context identity.ExternalContext
		want    error
	}{
		{identity.ExternalContext{Source: "feishu", ChatType: "group", AlternateSubject: "on_a"}, identity.ErrGroupIdentityMissing},
		{identity.ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm"}, identity.ErrIdentityMissing},
		{identity.ExternalContext{Source: "feishu", ChatType: "channel", ChatID: "oc"}, identity.ErrUnsupportedContext},
	} {
		if _, err := router.ResolveHermes(test.context); !errors.Is(err, test.want) {
			t.Fatalf("context %#v error = %v", test.context, err)
		}
	}
	card, err := router.ResolveHermes(groupContext("oc_group", "", "on_a"))
	if err != nil || card.UserID == "" || card.SessionID == "" {
		t.Fatalf("card fallback = %#v, %v", card, err)
	}
}

func groupContext(chatID, threadID, actor string) identity.ExternalContext {
	return identity.ExternalContext{Source: "feishu", ChatType: "group", ChatID: chatID, ThreadID: threadID, AlternateSubject: actor}
}
