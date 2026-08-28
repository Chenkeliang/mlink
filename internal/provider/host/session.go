package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

const (
	initializeTimeout      = 5 * time.Second
	healthTimeout          = 2 * time.Second
	recallTimeout          = 2 * time.Second
	captureTimeout         = 5 * time.Second
	shutdownTimeout        = 2 * time.Second
	writeTimeout           = 2 * time.Second
	terminateGrace         = 250 * time.Millisecond
	diagnosticBytes        = 64 << 10
	maxContextIDBytes      = 512
	maxContextKindBytes    = 128
	maxContextSourceBytes  = 1024
	maxContextTextBytes    = 256 << 10
	maxContextWarnings     = 8
	maxContextWarningBytes = 512
	defaultConcurrency     = 4
	maximumConcurrency     = 64
)

type startConfig struct {
	Snapshot          connection.ConnectionSnapshot
	Manifest          manifest.Manifest
	Argv              []string
	WorkingDirectory  string
	RuntimeDirectory  string
	Secrets           map[string]string
	ParentEnvironment []string
}

type processSession struct {
	route    connection.RouteKey
	snapshot connection.ConnectionSnapshot
	manifest manifest.Manifest

	stateMu      sync.RWMutex
	state        State
	capabilities map[string]manifest.CapabilityDescriptor
	faultCode    string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	nonce    string
	issued   atomic.Uint64
	expected atomic.Bool

	secrets   []string
	stderr    *redactingBuffer
	outbound  chan *outboundItem
	pendingMu sync.Mutex
	pending   map[string]*pendingCall
	inflight  chan struct{}

	done     chan struct{}
	doneOnce sync.Once
	exitMu   sync.RWMutex
	exit     ExitEvent
	exited   bool
}

type outboundState uint32

const (
	outboundQueued outboundState = iota
	outboundWriting
	outboundWritten
	outboundCanceled
)

type outboundItem struct {
	payload  json.RawMessage
	deadline time.Time
	state    atomic.Uint32
}

type pendingCall struct {
	item           *outboundItem
	response       chan callResult
	requestID      string
	idempotencyKey string
	capture        bool
}

type callResult struct {
	message protocol.Message
	err     error
}

func startSession(ctx context.Context, config startConfig) (*processSession, error) {
	if err := validateStartConfig(config); err != nil {
		return nil, err
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, errors.New("generate provider session nonce")
	}
	environment, _, err := buildEnvironment(config.ParentEnvironment, config.RuntimeDirectory)
	if err != nil {
		return nil, err
	}
	secretValues := make([]string, 0, len(config.Secrets))
	for _, value := range config.Secrets {
		secretValues = append(secretValues, value)
	}

	session := &processSession{
		route:        config.Snapshot.RouteKey(),
		snapshot:     config.Snapshot,
		manifest:     config.Manifest,
		state:        StateStarting,
		nonce:        hex.EncodeToString(nonceBytes),
		secrets:      secretValues,
		stderr:       newRedactingBuffer(secretValues, diagnosticBytes),
		outbound:     make(chan *outboundItem, maximumConcurrency),
		pending:      make(map[string]*pendingCall),
		done:         make(chan struct{}),
		capabilities: make(map[string]manifest.CapabilityDescriptor),
	}
	command := exec.Command(config.Argv[0], config.Argv[1:]...)
	command.Dir = config.WorkingDirectory
	command.Env = environment
	configureProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("create provider stdin")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("create provider stdout")
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, errors.New("create provider stderr")
	}
	session.cmd = command
	session.stdin = stdin
	session.stdout = stdout
	if err := command.Start(); err != nil {
		return nil, errors.New("start provider process")
	}
	go func() { _, _ = io.Copy(session.stderr, stderr) }()
	go session.writerLoop()
	go session.readerLoop()
	go session.waitLoop()

	session.setState(StateInitializing)
	initializeCtx, cancel := withDefaultDeadline(ctx, initializeTimeout)
	defer cancel()
	requestID, meta, id, err := session.nextRequest(initializeCtx, "")
	if err != nil {
		session.abortStart()
		return nil, err
	}
	params := protocol.InitializeParams{
		Meta:             meta,
		ProtocolVersions: []string{protocol.Version},
		Route:            session.route,
		Config:           config.Snapshot.Config(),
		Secrets:          cloneSecrets(config.Secrets),
	}
	message, err := session.callRPC(initializeCtx, id, requestID, "", "initialize", params, false)
	params.Secrets = nil
	if err != nil {
		session.abortStart()
		return nil, safeStartError("initialize provider", err)
	}
	var result protocol.InitializeResult
	if err := protocol.DecodeParams(message.Result, &result); err != nil {
		session.abortStart()
		return nil, errors.New("initialize provider: invalid result")
	}
	if result.ProviderID != session.route.ProviderID || result.ProviderVersion != session.route.ProviderVersion {
		session.abortStart()
		return nil, errors.New("initialize provider: provider identity mismatch")
	}
	if result.ProtocolVersion != protocol.Version || !containsString(config.Manifest.ProtocolVersions, protocol.Version) {
		session.abortStart()
		return nil, errors.New("initialize provider: incompatible protocol version")
	}
	capabilities, err := manifest.ValidateRuntime(config.Manifest.DeclaredCapabilities, result.Capabilities)
	if err != nil {
		session.abortStart()
		return nil, errors.New("initialize provider: runtime capability mismatch")
	}
	session.stateMu.Lock()
	session.capabilities = capabilities
	session.inflight = make(chan struct{}, minimumInFlight(capabilities))
	session.state = StateReady
	session.stateMu.Unlock()
	return session, nil
}

func validateStartConfig(config startConfig) error {
	if len(config.Argv) == 0 || config.Argv[0] == "" {
		return errors.New("provider entrypoint is empty")
	}
	if config.WorkingDirectory == "" || config.RuntimeDirectory == "" {
		return errors.New("provider directories are incomplete")
	}
	if len(config.Snapshot.Config()) > 1<<20 {
		return errors.New("provider config exceeds 1 MiB limit")
	}
	if len(config.Secrets) > 32 {
		return errors.New("provider secrets exceed 32 entries")
	}
	total := 0
	for name, value := range config.Secrets {
		if name == "" || len(name) > 64 || value == "" {
			return errors.New("provider secret metadata is invalid")
		}
		total += len(value)
	}
	if total > 64<<10 {
		return errors.New("provider secrets exceed 64 KiB limit")
	}
	if !containsString(config.Manifest.ProtocolVersions, protocol.Version) {
		return errors.New("provider manifest has no supported protocol version")
	}
	return nil
}

func (s *processSession) RouteKey() connection.RouteKey {
	return s.route
}

func (s *processSession) Capabilities() map[string]manifest.CapabilityDescriptor {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	result := make(map[string]manifest.CapabilityDescriptor, len(s.capabilities))
	for name, descriptor := range s.capabilities {
		descriptor.Scopes = append([]string(nil), descriptor.Scopes...)
		descriptor.Roles = append([]string(nil), descriptor.Roles...)
		result[name] = descriptor
	}
	return result
}

func (s *processSession) State() State {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state
}

func (s *processSession) Done() <-chan struct{} {
	return s.done
}

func (s *processSession) ExitEvent() (ExitEvent, bool) {
	s.exitMu.RLock()
	defer s.exitMu.RUnlock()
	return s.exit, s.exited
}

func (s *processSession) Health(ctx context.Context) (protocol.HealthResult, error) {
	descriptor, err := s.requireReadyCapability("health")
	if err != nil {
		return protocol.HealthResult{}, err
	}
	_ = descriptor
	callCtx, cancel := withDefaultDeadline(ctx, healthTimeout)
	defer cancel()
	release, err := s.acquireBusiness(callCtx)
	if err != nil {
		return protocol.HealthResult{}, err
	}
	defer release()
	requestID, meta, id, err := s.nextRequest(callCtx, "")
	if err != nil {
		return protocol.HealthResult{}, err
	}
	message, err := s.callRPC(callCtx, id, requestID, "", "health", protocol.HealthParams{Meta: meta}, false)
	if err != nil {
		return protocol.HealthResult{}, err
	}
	var result protocol.HealthResult
	if err := protocol.DecodeParams(message.Result, &result); err != nil || !validHealth(result) {
		s.fault(protocol.ErrorProtocol)
		return protocol.HealthResult{}, errors.New("provider returned an invalid health result")
	}
	return result, nil
}

func (s *processSession) CaptureTurn(ctx context.Context, meta CallMeta, turn model.Turn) (model.WriteReceipt, error) {
	descriptor, err := s.requireReadyCapability("capture_turn")
	if err != nil {
		return model.WriteReceipt{}, err
	}
	if turn.Identity.ConnectionID != s.route.ConnectionID {
		return model.WriteReceipt{}, errors.New("capture identity uses another connection")
	}
	if strings.TrimSpace(meta.IdempotencyKey) == "" {
		return model.WriteReceipt{}, errors.New("capture requires idempotency key")
	}
	for _, message := range turn.Messages {
		if !containsString(descriptor.Roles, message.Role) {
			return model.WriteReceipt{}, fmt.Errorf("capture message role %q is outside provider capability", message.Role)
		}
	}
	callCtx, cancel := withDefaultDeadline(ctx, captureTimeout)
	defer cancel()
	release, err := s.acquireBusiness(callCtx)
	if err != nil {
		return model.WriteReceipt{}, err
	}
	defer release()
	requestID, requestMeta, id, err := s.nextRequest(callCtx, meta.IdempotencyKey)
	if err != nil {
		return model.WriteReceipt{}, err
	}
	params := protocol.CaptureParams{Meta: requestMeta, Turn: turn}
	if err := validateRequestSize(params, descriptor.MaxRequestBytes); err != nil {
		return model.WriteReceipt{}, err
	}
	message, err := s.callRPC(callCtx, id, requestID, meta.IdempotencyKey, "capture_turn", params, true)
	if err != nil {
		return model.WriteReceipt{}, err
	}
	var result model.WriteReceipt
	if err := protocol.DecodeParams(message.Result, &result); err != nil ||
		result.ReceiptID != id ||
		(result.State != model.WriteAccepted && result.State != model.WriteVisible) ||
		(result.ReplaySafe && !descriptor.ReplaySafe) {
		s.fault(protocol.ErrorProtocol)
		return model.WriteReceipt{}, errors.New("provider returned an invalid write receipt")
	}
	return result, nil
}

func (s *processSession) Recall(ctx context.Context, _ CallMeta, request model.RecallRequest) (model.ContextBundle, error) {
	descriptor, err := s.requireReadyCapability("recall")
	if err != nil {
		return model.ContextBundle{}, err
	}
	if request.Identity.ConnectionID != s.route.ConnectionID {
		return model.ContextBundle{}, errors.New("recall identity uses another connection")
	}
	if request.MaxItems > descriptor.MaxResultItems {
		return model.ContextBundle{}, errors.New("recall max_items exceeds provider capability")
	}
	if !containsString(descriptor.Scopes, string(model.ScopeUser)) {
		return model.ContextBundle{}, errors.New("recall requires provider user scope")
	}
	if request.IncludeAgentShared && !containsString(descriptor.Scopes, string(model.ScopeAgent)) {
		return model.ContextBundle{}, errors.New("shared recall requires provider agent scope")
	}
	callCtx, cancel := withDefaultDeadline(ctx, recallTimeout)
	defer cancel()
	release, err := s.acquireBusiness(callCtx)
	if err != nil {
		return model.ContextBundle{}, err
	}
	defer release()
	requestID, meta, id, err := s.nextRequest(callCtx, "")
	if err != nil {
		return model.ContextBundle{}, err
	}
	params := protocol.RecallParams{Meta: meta, Request: request}
	if err := validateRequestSize(params, descriptor.MaxRequestBytes); err != nil {
		return model.ContextBundle{}, err
	}
	message, err := s.callRPC(callCtx, id, requestID, "", "recall", params, false)
	if err != nil {
		return model.ContextBundle{}, err
	}
	var result model.ContextBundle
	if err := protocol.DecodeParams(message.Result, &result); err != nil ||
		!validContextBundle(result, descriptor.MaxResultItems, descriptor.Scopes) {
		s.fault(protocol.ErrorProtocol)
		return model.ContextBundle{}, errors.New("provider returned an invalid recall result")
	}
	return result, nil
}

func (s *processSession) Shutdown(ctx context.Context) error {
	s.stateMu.Lock()
	if s.state == StateStopped {
		s.stateMu.Unlock()
		return nil
	}
	if s.state != StateReady {
		state := s.state
		s.stateMu.Unlock()
		return fmt.Errorf("provider session cannot shut down from state %s", state)
	}
	s.state = StateStopping
	s.expected.Store(true)
	s.stateMu.Unlock()

	callCtx, cancel := withDefaultDeadline(ctx, shutdownTimeout)
	defer cancel()
	requestID, meta, id, err := s.nextRequest(callCtx, "")
	if err == nil {
		_, err = s.callRPC(callCtx, id, requestID, "", "shutdown", protocol.ShutdownParams{Meta: meta}, false)
	}
	if err != nil {
		s.terminate()
		return err
	}
	_ = s.stdin.Close()
	select {
	case <-s.done:
		return nil
	case <-callCtx.Done():
		s.terminate()
		return callCtx.Err()
	}
}

func (s *processSession) acquireBusiness(ctx context.Context) (func(), error) {
	select {
	case s.inflight <- struct{}{}:
		return func() { <-s.inflight }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("provider session stopped")
	}
}

func (s *processSession) callRPC(
	ctx context.Context,
	id, requestID, idempotencyKey, method string,
	params any,
	capture bool,
) (protocol.Message, error) {
	payload, err := protocol.EncodeRequest(id, method, params)
	if err != nil {
		return protocol.Message{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		return protocol.Message{}, context.DeadlineExceeded
	}
	item := &outboundItem{payload: payload, deadline: deadline}
	pending := &pendingCall{
		item: item, response: make(chan callResult, 1), requestID: requestID,
		idempotencyKey: idempotencyKey, capture: capture,
	}
	s.pendingMu.Lock()
	s.pending[id] = pending
	s.pendingMu.Unlock()
	select {
	case s.outbound <- item:
	case <-ctx.Done():
		item.state.CompareAndSwap(uint32(outboundQueued), uint32(outboundCanceled))
		s.removePending(id, pending)
		return protocol.Message{}, ctx.Err()
	case <-s.done:
		s.removePending(id, pending)
		return protocol.Message{}, errors.New("provider session stopped")
	}

	select {
	case result := <-pending.response:
		return result.message, result.err
	case <-ctx.Done():
		if !s.removePending(id, pending) {
			result := <-pending.response
			return result.message, result.err
		}
		delivery := deliveryForCanceledItem(item)
		s.sendCancel(id)
		if capture && delivery != DeliveryNotSent {
			return protocol.Message{}, &CallError{
				Code: protocol.ErrorAmbiguousResult, RequestID: requestID, IdempotencyKey: idempotencyKey,
				Delivery: delivery, ReplaySafe: s.captureReplaySafe(), Message: "capture result is unknown",
			}
		}
		return protocol.Message{}, ctx.Err()
	case <-s.done:
		if !s.removePending(id, pending) {
			result := <-pending.response
			return result.message, result.err
		}
		return protocol.Message{}, errors.New("provider session stopped")
	}
}

func (s *processSession) nextRequest(ctx context.Context, idempotencyKey string) (string, protocol.RequestMeta, string, error) {
	if err := ctx.Err(); err != nil {
		return "", protocol.RequestMeta{}, "", err
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) {
		return "", protocol.RequestMeta{}, "", context.DeadlineExceeded
	}
	sequence := s.issued.Add(1)
	id := strconv.FormatUint(sequence, 10)
	requestID := s.nonce + "-" + id
	return requestID, protocol.RequestMeta{
		RequestID: requestID, DeadlineUnixMS: deadline.UnixMilli(), IdempotencyKey: idempotencyKey,
	}, id, nil
}

func (s *processSession) writerLoop() {
	encoder := protocol.NewEncoder(s.stdin)
	for {
		var item *outboundItem
		select {
		case <-s.done:
			return
		case item = <-s.outbound:
		}
		if !item.state.CompareAndSwap(uint32(outboundQueued), uint32(outboundWriting)) {
			continue
		}
		remaining := time.Until(item.deadline)
		if remaining <= 0 {
			s.fault(protocol.ErrorDeadlineExceeded)
			return
		}
		if remaining > writeTimeout {
			remaining = writeTimeout
		}
		written := make(chan error, 1)
		go func() { written <- encoder.WriteFrame(item.payload) }()
		timer := time.NewTimer(remaining)
		select {
		case err := <-written:
			if !timer.Stop() {
				<-timer.C
			}
			if err != nil {
				s.fault(protocol.ErrorProtocol)
				return
			}
			item.state.Store(uint32(outboundWritten))
		case <-timer.C:
			s.fault(protocol.ErrorDeadlineExceeded)
			return
		case <-s.done:
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func (s *processSession) readerLoop() {
	decoder := protocol.NewDecoder(s.stdout)
	for {
		raw, err := decoder.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) || !s.expected.Load() {
				s.fault(protocol.ErrorProtocol)
			}
			return
		}
		containsSecret, err := jsonContainsSecret(raw, s.secrets)
		if err != nil || containsSecret {
			s.fault(protocol.ErrorProtocol)
			return
		}
		message, err := protocol.ParseMessage(raw)
		if err != nil || message.Kind != protocol.MessageResponse {
			s.fault(protocol.ErrorProtocol)
			return
		}
		sequence, err := strconv.ParseUint(message.ID, 10, 64)
		if err != nil || sequence == 0 || sequence > s.issued.Load() {
			s.fault(protocol.ErrorProtocol)
			return
		}
		s.pendingMu.Lock()
		pending, exists := s.pending[message.ID]
		if exists {
			delete(s.pending, message.ID)
		}
		s.pendingMu.Unlock()
		if !exists {
			continue
		}
		if message.Error != nil {
			pending.response <- callResult{err: &CallError{
				Code: message.Error.ErrorCode, RequestID: pending.requestID,
				IdempotencyKey: pending.idempotencyKey, Delivery: DeliverySent,
				ReplaySafe: s.captureReplaySafe(), Message: safeCallMessage(message.Error.ErrorCode),
			}}
			continue
		}
		pending.response <- callResult{message: message}
	}
}

func (s *processSession) waitLoop() {
	err := s.cmd.Wait()
	exitCode := 0
	if s.cmd.ProcessState != nil {
		exitCode = s.cmd.ProcessState.ExitCode()
	}
	expected := s.expected.Load()
	s.stateMu.Lock()
	if expected {
		s.state = StateStopped
	} else {
		s.state = StateFaulted
		if s.faultCode == "" {
			s.faultCode = string(protocol.ErrorTemporarilyUnavailable)
		}
	}
	errorCode := s.faultCode
	s.stateMu.Unlock()
	if err == nil && expected {
		errorCode = ""
	}
	if expected {
		s.failPending(errors.New("provider process exited"))
	} else {
		s.failPendingForFault(protocol.ErrorCode(errorCode))
	}
	s.exitMu.Lock()
	s.exit = ExitEvent{Expected: expected, ExitCode: exitCode, ErrorCode: errorCode, OccurredAt: time.Now().UTC()}
	s.exited = true
	s.exitMu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *processSession) fault(code protocol.ErrorCode) {
	s.stateMu.Lock()
	if s.state == StateStopped || s.state == StateFaulted {
		s.stateMu.Unlock()
		return
	}
	s.state = StateFaulted
	s.faultCode = string(code)
	s.stateMu.Unlock()
	s.failPendingForFault(code)
	s.terminate()
}

func (s *processSession) failPending(err error) {
	s.pendingMu.Lock()
	pending := s.pending
	s.pending = make(map[string]*pendingCall)
	s.pendingMu.Unlock()
	for _, call := range pending {
		select {
		case call.response <- callResult{err: err}:
		default:
		}
	}
}

func (s *processSession) failPendingForFault(code protocol.ErrorCode) {
	s.pendingMu.Lock()
	pending := s.pending
	s.pending = make(map[string]*pendingCall)
	s.pendingMu.Unlock()
	for _, call := range pending {
		delivery := deliveryForCanceledItem(call.item)
		callCode := code
		message := "provider session faulted"
		if call.capture && delivery != DeliveryNotSent {
			callCode = protocol.ErrorAmbiguousResult
			message = "capture result is unknown"
		}
		failure := &CallError{
			Code: callCode, RequestID: call.requestID, IdempotencyKey: call.idempotencyKey,
			Delivery: delivery, ReplaySafe: s.captureReplaySafe(), Message: message,
		}
		select {
		case call.response <- callResult{err: failure}:
		default:
		}
	}
}

func (s *processSession) removePending(id string, expected *pendingCall) bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	current, exists := s.pending[id]
	if !exists || current != expected {
		return false
	}
	delete(s.pending, id)
	return true
}

func (s *processSession) sendCancel(id string) {
	payload, err := protocol.EncodeNotification("$/cancelRequest", protocol.CancelParams{ID: id})
	if err != nil {
		return
	}
	item := &outboundItem{payload: payload, deadline: time.Now().Add(writeTimeout)}
	select {
	case s.outbound <- item:
	default:
	}
}

func (s *processSession) requireReadyCapability(name string) (manifest.CapabilityDescriptor, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.state != StateReady {
		return manifest.CapabilityDescriptor{}, fmt.Errorf("provider session is not ready: %s", s.state)
	}
	descriptor, exists := s.capabilities[name]
	if !exists {
		return manifest.CapabilityDescriptor{}, errors.New("provider capability is unavailable")
	}
	return descriptor, nil
}

func (s *processSession) captureReplaySafe() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.capabilities["capture_turn"].ReplaySafe
}

func (s *processSession) setState(state State) {
	s.stateMu.Lock()
	s.state = state
	s.stateMu.Unlock()
}

func (s *processSession) abortStart() {
	s.expected.Store(true)
	s.terminate()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		if s.cmd.Process != nil {
			_ = signalProcessGroup(s.cmd.Process, syscall.SIGKILL)
		}
		<-s.done
	}
}

func (s *processSession) terminate() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = signalProcessGroup(s.cmd.Process, syscall.SIGTERM)
		process := s.cmd.Process
		go func() {
			timer := time.NewTimer(terminateGrace)
			defer timer.Stop()
			select {
			case <-s.done:
				return
			case <-timer.C:
				_ = signalProcessGroup(process, syscall.SIGKILL)
			}
		}()
	}
}

func deliveryForCanceledItem(item *outboundItem) DeliveryState {
	if item.state.CompareAndSwap(uint32(outboundQueued), uint32(outboundCanceled)) {
		return DeliveryNotSent
	}
	switch outboundState(item.state.Load()) {
	case outboundWritten:
		return DeliverySent
	case outboundWriting:
		return DeliveryMaybeSent
	default:
		return DeliveryNotSent
	}
}

func withDefaultDeadline(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= duration {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, duration)
}

func minimumInFlight(capabilities map[string]manifest.CapabilityDescriptor) int {
	minimum := maximumConcurrency
	for _, descriptor := range capabilities {
		value := descriptor.MaxInFlight
		if value <= 0 {
			value = defaultConcurrency
		}
		if value < minimum {
			minimum = value
		}
	}
	if minimum < 1 {
		return 1
	}
	return minimum
}

func validateRequestSize(value any, limit int64) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode provider request")
	}
	if int64(len(raw)) > limit {
		return errors.New("provider request exceeds capability limit")
	}
	return nil
}

func validHealth(result protocol.HealthResult) bool {
	if result.Process != "ready" && result.Process != "stopping" && result.Process != "faulted" {
		return false
	}
	if result.Config != "valid" && result.Config != "invalid" {
		return false
	}
	if result.Backend != "available" && result.Backend != "degraded" && result.Backend != "unavailable" {
		return false
	}
	if len(result.Diagnostics) > 8 {
		return false
	}
	for _, diagnostic := range result.Diagnostics {
		if len(diagnostic) > 256 {
			return false
		}
	}
	return true
}

func validContextBundle(bundle model.ContextBundle, maxItems int, allowedScopes []string) bool {
	if len(bundle.Items) > maxItems || len(bundle.Warnings) > maxContextWarnings {
		return false
	}
	for _, warning := range bundle.Warnings {
		if len(warning) > maxContextWarningBytes {
			return false
		}
	}
	seen := make(map[string]struct{}, len(bundle.Items))
	for _, item := range bundle.Items {
		if item.ID == "" || item.Source == "" || !containsString(allowedScopes, string(item.Scope)) {
			return false
		}
		if len(item.ID) > maxContextIDBytes || len(item.Kind) > maxContextKindBytes ||
			len(item.Source) > maxContextSourceBytes || len(item.Text) > maxContextTextBytes {
			return false
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}

func cloneSecrets(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func safeStartError(prefix string, err error) error {
	var callErr *CallError
	if errors.As(err, &callErr) {
		return fmt.Errorf("%s: %s", prefix, callErr.Code)
	}
	return fmt.Errorf("%s failed", prefix)
}

func safeCallMessage(code protocol.ErrorCode) string {
	switch code {
	case protocol.ErrorConfiguration:
		return "provider configuration is invalid"
	case protocol.ErrorAuthenticationFailed:
		return "provider authentication failed"
	case protocol.ErrorInvalidIdentity:
		return "provider rejected identity"
	case protocol.ErrorUnsupportedCapability:
		return "provider capability is unavailable"
	case protocol.ErrorDeadlineExceeded:
		return "provider request deadline exceeded"
	case protocol.ErrorRateLimited:
		return "provider rate limited the request"
	case protocol.ErrorTemporarilyUnavailable:
		return "provider is temporarily unavailable"
	case protocol.ErrorPermanentFailure:
		return "provider request failed"
	case protocol.ErrorAmbiguousResult:
		return "provider result is ambiguous"
	default:
		return "provider protocol error"
	}
}
