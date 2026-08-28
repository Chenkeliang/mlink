package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	if mode == "block-stdin-after-init" {
		serveInitializeThenBlock()
		return
	}
	if mode == "ignore-shutdown-sigterm" {
		signal.Ignore(syscall.SIGTERM)
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
	if h.mode == "private-health-error" {
		return protocol.HealthResult{}, &server.HandlerError{
			Code: protocol.ErrorTemporarilyUnavailable,
			Message: strings.Repeat("x", 1024) + " https://private.memory.invalid/secrets/token-file",
		}
	}
	diagnostics := []string(nil)
	if h.mode == "echo-health-secret" {
		diagnostics = []string{h.secret}
	}
	return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available", Diagnostics: diagnostics}, nil
}

func (h *fixtureHandler) CaptureTurn(ctx context.Context, _ protocol.CaptureParams) (model.WriteReceipt, error) {
	if h.mode == "block-capture" {
		fmt.Fprintln(os.Stderr, "CAPTURE_STARTED")
		<-ctx.Done()
		return model.WriteReceipt{}, ctx.Err()
	}
	return model.WriteReceipt{State: model.WriteAccepted, ProviderRefs: []string{"provider-ref-a"}}, nil
}

func (h *fixtureHandler) Recall(_ context.Context, params protocol.RecallParams) (model.ContextBundle, error) {
	return model.ContextBundle{Items: []model.ContextItem{{
		ID: "memory-a", Kind: "instruction", Scope: model.ScopeUser,
		Text: "先给结论", Source: "fixture:l1/memory-a",
	}}}, nil
}

func (h *fixtureHandler) Shutdown(context.Context, protocol.ShutdownParams) error {
	if h.mode == "ignore-shutdown-sigterm" {
		for {
			time.Sleep(time.Hour)
		}
	}
	return nil
}

func fixtureCapabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{
		"health": {Version: 1, MaxInFlight: 2},
		"capture_turn": {
			Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 3800 << 10,
			MaxInFlight: 2, ReplaySafe: false, Ordering: "turn",
		},
		"recall": {
			Version: 1, Scopes: []string{"user"}, MaxRequestBytes: 3800 << 10,
			MaxResultItems: 5, MaxInFlight: 2,
		},
	}
}

func serveInitializeThenBlock() {
	decoder := protocol.NewDecoder(os.Stdin)
	raw, err := decoder.ReadFrame()
	if err != nil {
		os.Exit(3)
	}
	message, err := protocol.ParseMessage(raw)
	if err != nil || message.Method != "initialize" {
		os.Exit(4)
	}
	result := protocol.InitializeResult{
		ProviderID: "dev.mlink.fixture", ProviderVersion: "0.1.0",
		ProtocolVersion: protocol.Version, Capabilities: fixtureCapabilities(),
	}
	response, err := protocol.EncodeResult(message.ID, result)
	if err != nil || protocol.NewEncoder(os.Stdout).WriteFrame(response) != nil {
		os.Exit(5)
	}
	for {
		time.Sleep(time.Hour)
	}
}
