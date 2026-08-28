package server

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

func TestServerRequiresInitializeFirst(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- New(&testHandler{}).Serve(context.Background(), inputReader, outputWriter)
	}()

	request, err := protocol.EncodeRequest("1", "health", protocol.HealthParams{Meta: futureMeta("health-before-init")})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.NewEncoder(inputWriter).WriteFrame(request); err != nil {
		t.Fatal(err)
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()

	select {
	case err := <-serveDone:
		if err == nil {
			t.Fatal("Serve() error = nil, want initialize ordering error")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not reject a pre-initialize request")
	}
}

func TestServerDispatchesLifecycle(t *testing.T) {
	handler := &testHandler{}
	harness := newHarness(t, handler)
	harness.initialize(t)

	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-2")})
	health := harness.readResponse(t)
	if health.ID != "2" || health.Error != nil {
		t.Fatalf("health response = %#v", health)
	}
	var healthResult protocol.HealthResult
	if err := protocol.DecodeParams(health.Result, &healthResult); err != nil {
		t.Fatalf("decode health result: %v", err)
	}
	if healthResult.Process != "ready" || healthResult.Backend != "available" {
		t.Fatalf("health result = %#v", healthResult)
	}

	harness.sendRequest(t, "3", "capture_turn", protocol.CaptureParams{
		Meta: futureMeta("capture-3"),
		Turn: model.Turn{Identity: model.IdentityScope{
			TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", TurnID: "turn-a",
		}, Messages: []model.Message{{Role: "user", Content: "hello"}}},
	})
	capture := harness.readResponse(t)
	if capture.ID != "3" || capture.Error != nil {
		t.Fatalf("capture response = %#v", capture)
	}

	harness.shutdown(t, "4")
	if got := handler.callNames(); len(got) != 4 || got[0] != "initialize" || got[1] != "health" || got[2] != "capture" || got[3] != "shutdown" {
		t.Fatalf("handler calls = %#v", got)
	}
}

func TestServerCancelRequestCancelsHandlerContext(t *testing.T) {
	started := make(chan struct{})
	handler := &testHandler{health: func(ctx context.Context, _ protocol.HealthParams) (protocol.HealthResult, error) {
		close(started)
		<-ctx.Done()
		return protocol.HealthResult{}, ctx.Err()
	}}
	harness := newHarness(t, handler)
	harness.initialize(t)

	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-cancel")})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("health handler did not start")
	}
	harness.sendNotification(t, "$/cancelRequest", protocol.CancelParams{ID: "2"})
	response := harness.readResponse(t)
	if response.ID != "2" || response.Error == nil || response.Error.ErrorCode != protocol.ErrorDeadlineExceeded {
		t.Fatalf("canceled response = %#v", response)
	}
	harness.shutdown(t, "3")
}

func TestServerEnforcesNegotiatedConcurrencyWithoutBlockingCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := &testHandler{
		capabilities: map[string]manifest.CapabilityDescriptor{
			"health": {Version: 1, MaxInFlight: 1},
		},
		health: func(ctx context.Context, _ protocol.HealthParams) (protocol.HealthResult, error) {
			started <- struct{}{}
			select {
			case <-release:
				return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available"}, nil
			case <-ctx.Done():
				return protocol.HealthResult{}, ctx.Err()
			}
		},
	}
	harness := newHarness(t, handler)
	harness.initialize(t)

	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-first")})
	<-started
	harness.sendRequest(t, "3", "health", protocol.HealthParams{Meta: futureMeta("health-second")})
	second := harness.readResponse(t)
	if second.ID != "3" || second.Error == nil || second.Error.ErrorCode != protocol.ErrorRateLimited {
		t.Fatalf("second response = %#v", second)
	}
	close(release)
	first := harness.readResponse(t)
	if first.ID != "2" || first.Error != nil {
		t.Fatalf("first response = %#v", first)
	}
	harness.shutdown(t, "4")
}

func TestServerUsesProtocolDeadline(t *testing.T) {
	called := false
	handler := &testHandler{health: func(context.Context, protocol.HealthParams) (protocol.HealthResult, error) {
		called = true
		return protocol.HealthResult{}, nil
	}}
	harness := newHarness(t, handler)
	harness.initialize(t)

	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: protocol.RequestMeta{
		RequestID: "expired", DeadlineUnixMS: time.Now().Add(-time.Second).UnixMilli(),
	}})
	response := harness.readResponse(t)
	if response.Error == nil || response.Error.ErrorCode != protocol.ErrorDeadlineExceeded {
		t.Fatalf("expired response = %#v", response)
	}
	if called {
		t.Fatal("expired request reached handler")
	}
	harness.shutdown(t, "3")
}

func TestServerStopsAfterInitializeFailure(t *testing.T) {
	handler := &testHandler{initializeErr: &HandlerError{
		Code: protocol.ErrorConfiguration, Message: "provider configuration is invalid",
	}}
	harness := newHarness(t, handler)
	harness.sendRequest(t, "1", "initialize", protocol.InitializeParams{
		Meta: protocol.RequestMeta{RequestID: "initialize-failure", DeadlineUnixMS: time.Now().Add(time.Second).UnixMilli()},
	})
	response := harness.readResponse(t)
	if response.Error == nil || response.Error.ErrorCode != protocol.ErrorConfiguration {
		t.Fatalf("initialize response = %#v", response)
	}
	select {
	case err := <-harness.serveDone:
		harness.stopped = true
		if err == nil {
			t.Fatal("Serve() error = nil, want initialize failure")
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() continued after initialize failure")
	}
}

func TestServerRejectsDuplicateRequestID(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := &testHandler{
		capabilities: map[string]manifest.CapabilityDescriptor{
			"health": {Version: 1, MaxInFlight: 1},
		},
		health: func(ctx context.Context, _ protocol.HealthParams) (protocol.HealthResult, error) {
			started <- struct{}{}
			select {
			case <-release:
				return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available"}, nil
			case <-ctx.Done():
				return protocol.HealthResult{}, ctx.Err()
			}
		},
	}
	harness := newHarness(t, handler)
	harness.initialize(t)
	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-first")})
	<-started
	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-duplicate")})
	duplicate := harness.readResponse(t)
	if duplicate.ID != "2" || duplicate.Error == nil || duplicate.Error.ErrorCode != protocol.ErrorProtocol {
		t.Fatalf("duplicate response = %#v", duplicate)
	}
	close(release)
	first := harness.readResponse(t)
	if first.ID != "2" || first.Error != nil {
		t.Fatalf("first response = %#v", first)
	}
	harness.shutdown(t, "3")
}

func TestServerShutdownWaitsForActiveHandlersAndRejectsNewCalls(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := &testHandler{health: func(ctx context.Context, _ protocol.HealthParams) (protocol.HealthResult, error) {
		started <- struct{}{}
		select {
		case <-release:
			return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available"}, nil
		case <-ctx.Done():
			return protocol.HealthResult{}, ctx.Err()
		}
	}}
	harness := newHarness(t, handler)
	harness.initialize(t)
	harness.sendRequest(t, "2", "health", protocol.HealthParams{Meta: futureMeta("health-active")})
	<-started
	harness.sendRequest(t, "3", "shutdown", protocol.ShutdownParams{Meta: futureMeta("shutdown-wait")})
	harness.sendRequest(t, "4", "health", protocol.HealthParams{Meta: futureMeta("health-after-shutdown")})
	rejected := harness.readResponse(t)
	if rejected.ID != "4" || rejected.Error == nil || rejected.Error.ErrorCode != protocol.ErrorTemporarilyUnavailable {
		t.Fatalf("post-shutdown response = %#v", rejected)
	}

	type asyncResponse struct {
		message protocol.Message
		err     error
	}
	next := make(chan asyncResponse, 1)
	go func() {
		raw, err := harness.decoder.ReadFrame()
		if err != nil {
			next <- asyncResponse{err: err}
			return
		}
		message, err := protocol.ParseMessage(raw)
		next <- asyncResponse{message: message, err: err}
	}()
	select {
	case response := <-next:
		t.Fatalf("response %#v arrived before active handler completed", response)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	asyncFirst := <-next
	if asyncFirst.err != nil {
		t.Fatalf("read response: %v", asyncFirst.err)
	}
	first := asyncFirst.message
	second := harness.readResponse(t)
	responses := map[string]protocol.Message{first.ID: first, second.ID: second}
	for _, id := range []string{"2", "3"} {
		if response, exists := responses[id]; !exists || response.Error != nil {
			t.Fatalf("response %s = %#v", id, response)
		}
	}
	_ = harness.inputWriter.Close()
	select {
	case err := <-harness.serveDone:
		harness.stopped = true
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after shutdown")
	}
}

type testHandler struct {
	mu            sync.Mutex
	calls         []string
	capabilities  map[string]manifest.CapabilityDescriptor
	health        func(context.Context, protocol.HealthParams) (protocol.HealthResult, error)
	initializeErr error
}

func (h *testHandler) Initialize(context.Context, protocol.InitializeParams) (protocol.InitializeResult, error) {
	h.record("initialize")
	if h.initializeErr != nil {
		return protocol.InitializeResult{}, h.initializeErr
	}
	capabilities := h.capabilities
	if capabilities == nil {
		capabilities = map[string]manifest.CapabilityDescriptor{
			"health":       {Version: 1, MaxInFlight: 2},
			"capture_turn": {Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 1024, MaxInFlight: 2, Ordering: "turn"},
			"recall":       {Version: 1, Scopes: []string{"user"}, MaxRequestBytes: 1024, MaxResultItems: 5, MaxInFlight: 2},
		}
	}
	return protocol.InitializeResult{
		ProviderID: "dev.mlink.fixture", ProviderVersion: "0.1.0", ProtocolVersion: "1.0", Capabilities: capabilities,
	}, nil
}

func (h *testHandler) Health(ctx context.Context, params protocol.HealthParams) (protocol.HealthResult, error) {
	h.record("health")
	if h.health != nil {
		return h.health(ctx, params)
	}
	return protocol.HealthResult{Process: "ready", Config: "valid", Backend: "available"}, nil
}

func (h *testHandler) CaptureTurn(context.Context, protocol.CaptureParams) (model.WriteReceipt, error) {
	h.record("capture")
	return model.WriteReceipt{ReceiptID: "3", State: model.WriteAccepted}, nil
}

func (h *testHandler) Recall(context.Context, protocol.RecallParams) (model.ContextBundle, error) {
	h.record("recall")
	return model.ContextBundle{}, nil
}

func (h *testHandler) Shutdown(context.Context, protocol.ShutdownParams) error {
	h.record("shutdown")
	return nil
}

func (h *testHandler) record(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, name)
}

func (h *testHandler) callNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

type harness struct {
	t            *testing.T
	inputWriter  *io.PipeWriter
	outputReader *io.PipeReader
	encoder      *protocol.Encoder
	decoder      *protocol.Decoder
	serveDone    chan error
	stopped      bool
}

func newHarness(t *testing.T, handler Handler) *harness {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &harness{
		t: t, inputWriter: inputWriter, outputReader: outputReader,
		encoder: protocol.NewEncoder(inputWriter), decoder: protocol.NewDecoder(outputReader), serveDone: make(chan error, 1),
	}
	go func() {
		h.serveDone <- New(handler).Serve(context.Background(), inputReader, outputWriter)
	}()
	t.Cleanup(func() {
		_ = inputWriter.Close()
		_ = outputReader.Close()
		if h.stopped {
			return
		}
		select {
		case <-h.serveDone:
		case <-time.After(time.Second):
			t.Error("Provider Server did not stop")
		}
	})
	return h
}

func (h *harness) initialize(t *testing.T) {
	t.Helper()
	h.sendRequest(t, "1", "initialize", protocol.InitializeParams{
		Meta:             protocol.RequestMeta{RequestID: "initialize-1", DeadlineUnixMS: time.Now().Add(time.Second).UnixMilli()},
		ProtocolVersions: []string{"1.0"},
	})
	response := h.readResponse(t)
	if response.ID != "1" || response.Error != nil {
		t.Fatalf("initialize response = %#v", response)
	}
}

func (h *harness) shutdown(t *testing.T, id string) {
	t.Helper()
	h.sendRequest(t, id, "shutdown", protocol.ShutdownParams{Meta: futureMeta("shutdown-" + id)})
	response := h.readResponse(t)
	if response.ID != id || response.Error != nil {
		t.Fatalf("shutdown response = %#v", response)
	}
	_ = h.inputWriter.Close()
	select {
	case err := <-h.serveDone:
		h.stopped = true
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after shutdown")
	}
}

func (h *harness) sendRequest(t *testing.T, id, method string, params any) {
	t.Helper()
	raw, err := protocol.EncodeRequest(id, method, params)
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	if err := h.encoder.WriteFrame(raw); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
}

func (h *harness) sendNotification(t *testing.T, method string, params any) {
	t.Helper()
	raw, err := protocol.EncodeNotification(method, params)
	if err != nil {
		t.Fatalf("EncodeNotification() error = %v", err)
	}
	if err := h.encoder.WriteFrame(raw); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
}

func (h *harness) readResponse(t *testing.T) protocol.Message {
	t.Helper()
	raw, err := h.decoder.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	message, err := protocol.ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if message.Kind != protocol.MessageResponse {
		t.Fatalf("message kind = %v, want response", message.Kind)
	}
	return message
}

func futureMeta(requestID string) protocol.RequestMeta {
	return protocol.RequestMeta{RequestID: requestID, DeadlineUnixMS: time.Now().Add(2 * time.Second).UnixMilli()}
}
