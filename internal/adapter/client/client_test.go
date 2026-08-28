package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mlink/internal/broker"
)

func TestRecallTimeoutReturnsEmptyFailOpenBundle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		time.Sleep(200 * time.Millisecond)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, AdapterID: "codex", RecallTimeout: 20 * time.Millisecond}
	bundle, err := client.Recall(context.Background(), broker.RecallInput{Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) != 0 || len(bundle.Warnings) == 0 {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestRecallConnectionFailureReturnsEmptyFailOpenBundle(t *testing.T) {
	client := Client{BaseURL: "http://127.0.0.1:1", AdapterID: "codex", RecallTimeout: 100 * time.Millisecond}
	bundle, err := client.Recall(context.Background(), broker.RecallInput{Query: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) != 0 || len(bundle.Warnings) == 0 {
		t.Fatalf("bundle = %#v", bundle)
	}
	if got := bundle.Warnings[0]; got != "MLink recall unavailable; continuing without external memory" {
		t.Fatalf("warning = %q", got)
	}
}

func TestClientSendsAdapterAndBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"items":[],"partial":false}`))
	}))
	defer server.Close()
	client := Client{BaseURL: server.URL, AdapterID: "hermes", Token: "token", RecallTimeout: time.Second}
	if _, err := client.Recall(context.Background(), broker.RecallInput{Source: "feishu", SourceSubject: "ou_a", Query: "hello"}); err != nil {
		t.Fatal(err)
	}
}
