package hermes

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"mlink/internal/identity"
)

func TestDetectIdentityCandidatesDeduplicatesAndPrefersUnionID(t *testing.T) {
	args, err := candidateCommand("hermes-agent-env", "/home/test/.hermes/state.db", 50)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeOrbRunner{results: map[string][]byte{
		joinArgs(args): []byte(`[
          {"display_name":"陈科良","kind":"union_id","value":"on_owner","last_seen":20},
          {"display_name":"陈科良","kind":"union_id","value":"on_owner","last_seen":10},
          {"display_name":"其他用户","kind":"user_id","value":"u_other","last_seen":15}
        ]`),
	}}
	got, err := DetectIdentityCandidates(context.Background(), runner, "hermes-agent-env", "/home/test/.hermes", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "union_id" || string(got[0].Value) != "on_owner" || got[0].Suffix != "wner" {
		t.Fatalf("candidates = %#v", got)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestDetectIdentityCandidatesRejectsUnsafeHomeAndLimit(t *testing.T) {
	for _, test := range []struct {
		home  string
		limit int
	}{{"/", 50}, {"relative", 50}, {"/home/test/.hermes", 0}, {"/home/test/.hermes", 201}} {
		if _, err := DetectIdentityCandidates(context.Background(), &fakeOrbRunner{}, "hermes-agent-env", test.home, test.limit); err == nil {
			t.Fatalf("DetectIdentityCandidates(%q,%d) error = nil", test.home, test.limit)
		}
	}
}

func TestCandidateFormattingRedactsValue(t *testing.T) {
	candidate := identity.NewCandidate("陈科良", "union_id", "on_actual_secret", time.Unix(20, 0))
	for _, rendered := range []string{fmt.Sprint(candidate), fmt.Sprintf("%#v", candidate)} {
		if strings.Contains(rendered, "on_actual_secret") || !strings.Contains(rendered, candidate.Suffix) {
			t.Fatalf("rendered candidate = %q", rendered)
		}
	}
}
