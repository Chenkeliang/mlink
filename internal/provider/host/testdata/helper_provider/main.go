package main

import (
	"context"
	"os"
	"strings"

	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
	"mlink/internal/provider/server"
)

func main() {
	mode := "normal"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	handler := &fixtureHandler{mode: mode}
	if err := server.New(handler).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
}

type fixtureHandler struct {
	mode   string
	secret string
}

func (h *fixtureHandler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	h.secret = params.Secrets["token"]
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_BASE_URL") != "" {
		return protocol.InitializeResult{}, &server.HandlerError{
			Code: protocol.ErrorConfiguration, Message: "agent model environment leaked",
		}
	}
	for _, argument := range os.Args {
		if h.secret != "" && strings.Contains(argument, h.secret) {
			return protocol.InitializeResult{}, &server.HandlerError{
				Code: protocol.ErrorConfiguration, Message: "secret leaked into argv",
			}
		}
	}
	providerID := "dev.mlink.fixture"
	if h.mode == "wrong-provider" {
		providerID = "dev.mlink.wrong"
	}
	if h.mode == "echo-initialize-secret" {
		providerID = h.secret
	}
	return protocol.InitializeResult{
		ProviderID: providerID, ProviderVersion: "0.1.0", ProtocolVersion: protocol.Version,
		Capabilities: fixtureCapabilities(),
	}, nil
}

func (h *fixtureHandler) Health(context.Context, protocol.HealthParams) (protocol.HealthResult, error) {
	diagnostics := []string(nil)
	if h.mode == "echo-health-secret" {
		diagnostics = []string{h.secret}
	}
	return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available", Diagnostics: diagnostics}, nil
}

func (h *fixtureHandler) CaptureTurn(context.Context, protocol.CaptureParams) (model.WriteReceipt, error) {
	return model.WriteReceipt{State: model.WriteAccepted, ProviderRefs: []string{"provider-ref-a"}}, nil
}

func (h *fixtureHandler) Recall(_ context.Context, params protocol.RecallParams) (model.ContextBundle, error) {
	return model.ContextBundle{Items: []model.ContextItem{{
		ID: "memory-a", Kind: "instruction", Scope: model.ScopeUser,
		Text: "先给结论", Source: "fixture:l1/memory-a",
	}}}, nil
}

func (h *fixtureHandler) Shutdown(context.Context, protocol.ShutdownParams) error {
	return nil
}

func fixtureCapabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{
		"health": {Version: 1, MaxInFlight: 2},
		"capture_turn": {
			Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 1 << 20,
			MaxInFlight: 2, ReplaySafe: false, Ordering: "turn",
		},
		"recall": {
			Version: 1, Scopes: []string{"user"}, MaxRequestBytes: 1 << 20,
			MaxResultItems: 5, MaxInFlight: 2,
		},
	}
}
