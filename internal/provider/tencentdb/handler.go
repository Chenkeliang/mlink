package tencentdb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
	"mlink/internal/provider/server"
)

const (
	providerID      = "dev.mlink.tencentdb"
	providerVersion = "0.1.0"
)

type ServerHandler struct {
	mu       sync.RWMutex
	client   *Client
	provider *Provider
}

func NewServerHandler() *ServerHandler {
	return &ServerHandler{}
}

func (h *ServerHandler) Initialize(_ context.Context, params protocol.InitializeParams) (protocol.InitializeResult, error) {
	var config struct {
		BaseURL   string `json:"base_url"`
		ServiceID string `json:"service_id"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := protocol.DecodeParams(params.Config, &config); err != nil {
		return protocol.InitializeResult{}, configurationError("invalid TencentDB configuration")
	}
	if config.TimeoutMS < 100 || config.TimeoutMS > 30_000 {
		return protocol.InitializeResult{}, configurationError("TencentDB timeout must be between 100 and 30000 milliseconds")
	}
	if !safeMemoryCoreEndpoint(config.BaseURL) {
		return protocol.InitializeResult{}, configurationError("TencentDB endpoint must use HTTPS or loopback HTTP")
	}
	token := params.Secrets["token"]
	if strings.TrimSpace(token) == "" {
		return protocol.InitializeResult{}, configurationError("TencentDB token is missing")
	}
	client, err := NewClient(Config{
		BaseURL: config.BaseURL, Token: token, ServiceID: config.ServiceID,
		HTTPClient: &http.Client{
			Timeout: time.Duration(config.TimeoutMS) * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	})
	if err != nil {
		return protocol.InitializeResult{}, configurationError("invalid TencentDB configuration")
	}
	h.mu.Lock()
	h.client = client
	h.provider = NewProvider(client)
	h.mu.Unlock()
	return protocol.InitializeResult{
		ProviderID: providerID, ProviderVersion: providerVersion, ProtocolVersion: protocol.Version,
		Capabilities: tencentDBCapabilities(),
	}, nil
}

func (h *ServerHandler) Health(ctx context.Context, _ protocol.HealthParams) (protocol.HealthResult, error) {
	client, _, err := h.current()
	if err != nil {
		return protocol.HealthResult{}, err
	}
	result := protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available"}
	if err := client.health(ctx); err != nil {
		if ctx.Err() != nil {
			return protocol.HealthResult{}, ctx.Err()
		}
		result.Backend = "unavailable"
		result.Diagnostics = []string{"MemoryCore health check failed"}
	}
	return result, nil
}

func (h *ServerHandler) CaptureTurn(ctx context.Context, params protocol.CaptureParams) (model.WriteReceipt, error) {
	_, provider, err := h.current()
	if err != nil {
		return model.WriteReceipt{}, err
	}
	receipt, err := provider.CaptureTurn(ctx, params.Turn)
	if err != nil {
		return model.WriteReceipt{}, mapProviderError(err)
	}
	return receipt, nil
}

func (h *ServerHandler) Recall(ctx context.Context, params protocol.RecallParams) (model.ContextBundle, error) {
	_, provider, err := h.current()
	if err != nil {
		return model.ContextBundle{}, err
	}
	bundle, err := provider.Recall(ctx, params.Request)
	if err != nil {
		return model.ContextBundle{}, mapProviderError(err)
	}
	return bundle, nil
}

func (h *ServerHandler) Shutdown(context.Context, protocol.ShutdownParams) error {
	h.mu.Lock()
	h.client = nil
	h.provider = nil
	h.mu.Unlock()
	return nil
}

func (h *ServerHandler) current() (*Client, *Provider, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.client == nil || h.provider == nil {
		return nil, nil, configurationError("TencentDB Provider is not initialized")
	}
	return h.client, h.provider, nil
}

func safeMemoryCoreEndpoint(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "localhost" {
		return true
	}
	address := net.ParseIP(hostname)
	return address != nil && address.IsLoopback()
}

func tencentDBCapabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{
		"health": {Version: 1, MaxInFlight: 4},
		"capture_turn": {
			Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 1 << 20,
			MaxInFlight: 4, ReplaySafe: false, Ordering: "turn",
		},
		"recall": {
			Version: 1, Scopes: []string{"user", "agent"}, MaxRequestBytes: 256 << 10,
			MaxResultItems: 20, MaxInFlight: 4,
		},
	}
}

func configurationError(message string) *server.HandlerError {
	return &server.HandlerError{Code: protocol.ErrorConfiguration, Message: message}
}

func mapProviderError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.HTTPStatus == http.StatusForbidden:
			return &server.HandlerError{Code: protocol.ErrorAuthenticationFailed, Message: "MemoryCore authentication failed"}
		case apiErr.HTTPStatus == http.StatusTooManyRequests:
			return &server.HandlerError{Code: protocol.ErrorRateLimited, Message: "MemoryCore rate limited the request"}
		case apiErr.HTTPStatus >= 500:
			return &server.HandlerError{Code: protocol.ErrorTemporarilyUnavailable, Message: "MemoryCore is temporarily unavailable"}
		default:
			return &server.HandlerError{Code: protocol.ErrorPermanentFailure, Message: "MemoryCore rejected the request"}
		}
	}
	return &server.HandlerError{Code: protocol.ErrorTemporarilyUnavailable, Message: "MemoryCore request failed"}
}
