package cursor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"mlink/internal/broker"
	"mlink/internal/model"
)

type fakeClient struct {
	recalls   []broker.RecallInput
	fragments []broker.FragmentInput
	flushes   []string
	err       error
}

func (client *fakeClient) Recall(_ context.Context, input broker.RecallInput) (model.ContextBundle, error) {
	client.recalls = append(client.recalls, input)
	return model.ContextBundle{Items: []model.ContextItem{{Text: "Owner prefers concise technical answers."}}}, client.err
}

func (client *fakeClient) SubmitFragment(_ context.Context, input broker.FragmentInput) error {
	client.fragments = append(client.fragments, input)
	return client.err
}

func (client *fakeClient) Flush(_ context.Context, session string, _ broker.FlushInput) (int, error) {
	client.flushes = append(client.flushes, session)
	return 0, client.err
}

func TestHandleOfficialCursorLifecycleFixtures(t *testing.T) {
	client := &fakeClient{}
	for _, testCase := range []struct {
		event, fixture string
	}{
		{"sessionStart", "testdata/session-start.json"},
		{"beforeSubmitPrompt", "testdata/before-submit.json"},
		{"afterAgentResponse", "testdata/after-response.json"},
	} {
		input, err := os.ReadFile(testCase.fixture)
		if err != nil {
			t.Fatal(err)
		}
		output := &bytes.Buffer{}
		if err := Handle(context.Background(), testCase.event, bytes.NewReader(input), output, client); err != nil {
			t.Fatalf("Handle(%s): %v", testCase.event, err)
		}
		if output.Len() == 0 {
			t.Fatalf("Handle(%s) emitted no JSON", testCase.event)
		}
	}
	if len(client.recalls) != 1 || len(client.fragments) != 2 {
		t.Fatalf("recalls/fragments = %#v/%#v", client.recalls, client.fragments)
	}
	if client.fragments[0].SessionID != "conv-1" || client.fragments[0].TurnID != "gen-1" || client.fragments[0].Role != "user" || client.fragments[1].Role != "assistant" {
		t.Fatalf("fragments = %#v", client.fragments)
	}
}

func TestHandleCursorHooksFailOpenOnBrokerFailure(t *testing.T) {
	client := &fakeClient{err: errors.New("broker unavailable")}
	for _, testCase := range []struct{ event, fixture string }{{"beforeSubmitPrompt", "testdata/before-submit.json"}, {"afterAgentResponse", "testdata/after-response.json"}} {
		input, _ := os.ReadFile(testCase.fixture)
		output := &bytes.Buffer{}
		if err := Handle(context.Background(), testCase.event, bytes.NewReader(input), output, client); err != nil {
			t.Fatalf("Handle(%s) did not fail open: %v", testCase.event, err)
		}
	}
}

func TestHandleCursorSessionEndFlushesCanceledFragments(t *testing.T) {
	client := &fakeClient{}
	input := `{"conversation_id":"conv-1","generation_id":"gen-2","hook_event_name":"sessionEnd","session_id":"conv-1","reason":"aborted","duration_ms":42,"is_background_agent":false,"final_status":"aborted"}`
	if err := Handle(context.Background(), "sessionEnd", strings.NewReader(input), &bytes.Buffer{}, client); err != nil {
		t.Fatal(err)
	}
	if len(client.flushes) != 1 || client.flushes[0] != "conv-1" {
		t.Fatalf("flushes = %#v", client.flushes)
	}
}

func TestHandleCursorRejectsMalformedAndOversizeInput(t *testing.T) {
	for name, input := range map[string]string{"malformed": `{`, "oversize": `{"conversation_id":"` + strings.Repeat("x", maxHookInputBytes) + `"}`} {
		t.Run(name, func(t *testing.T) {
			if err := Handle(context.Background(), "sessionStart", strings.NewReader(input), &bytes.Buffer{}, &fakeClient{}); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
