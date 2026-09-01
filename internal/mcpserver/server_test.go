package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mlink/internal/broker"
	"mlink/internal/model"
)

type fakeBackend struct {
	recalls []broker.RecallInput
	err     error
}

func (backend *fakeBackend) Recall(ctx context.Context, input broker.RecallInput) (model.ContextBundle, error) {
	if err := ctx.Err(); err != nil {
		return model.ContextBundle{}, err
	}
	backend.recalls = append(backend.recalls, input)
	return model.ContextBundle{Items: []model.ContextItem{{ID: "l1:one", Kind: "preference", Scope: model.ScopeUser, Text: "Use exact Plan IDs.", Source: "tencentdb:l1/one"}}}, backend.err
}

func (backend *fakeBackend) Status(context.Context) error { return backend.err }

func connectTestClient(t *testing.T, backend Backend) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := New(backend, "test")
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func TestServerListsAndCallsMemoryTools(t *testing.T) {
	backend := &fakeBackend{}
	session := connectTestClient(t, backend)
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 2 || tools.Tools[0].Name != "mlink_memory_search" || tools.Tools[1].Name != "mlink_memory_status" {
		t.Fatalf("tools/error = %#v/%v", tools, err)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mlink_memory_search", Arguments: map[string]any{"query": "release safety", "limit": 5}})
	if err != nil || result.IsError || len(backend.recalls) != 1 || backend.recalls[0].Query != "release safety" || backend.recalls[0].MaxItems != 5 {
		t.Fatalf("result/error/recalls = %#v/%v/%#v", result, err, backend.recalls)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "untrusted historical memory") || !strings.Contains(text, "Use exact Plan IDs") {
		t.Fatalf("text = %q", text)
	}
	status, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mlink_memory_status", Arguments: map[string]any{}})
	if err != nil || status.IsError || !strings.Contains(status.Content[0].(*mcp.TextContent).Text, "available") {
		t.Fatalf("status/error = %#v/%v", status, err)
	}
}

func TestServerRejectsSearchBoundsAndReportsBackendFailure(t *testing.T) {
	backend := &fakeBackend{err: errors.New("offline")}
	session := connectTestClient(t, backend)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mlink_memory_search", Arguments: map[string]any{"query": "x", "limit": 21}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result = %#v", result)
	}
	status, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "mlink_memory_status", Arguments: map[string]any{}})
	if err != nil || !status.IsError {
		t.Fatalf("status/error = %#v/%v", status, err)
	}
}

func TestServerPropagatesCanceledContext(t *testing.T) {
	backend := &fakeBackend{}
	server := New(backend, "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := search(ctx, nil, SearchInput{Query: "cancel", Limit: 5}, backend)
	if !errors.Is(err, context.Canceled) || server == nil {
		t.Fatalf("error/server = %v/%v", err, server)
	}
}
