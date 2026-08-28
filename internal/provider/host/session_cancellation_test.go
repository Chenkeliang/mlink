package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

func TestSessionCaptureCancellationAfterProviderReceivedIsAmbiguousSent(t *testing.T) {
	session := startFixtureSession(t, "block-capture", "token-capture-sent")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	result := make(chan error, 1)
	go func() {
		_, err := session.CaptureTurn(ctx, CallMeta{IdempotencyKey: "capture-sent"}, largeTurn("small"))
		result <- err
	}()
	waitForDiagnostic(t, session, "CAPTURE_STARTED")
	cancel()
	err := <-result
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Code != protocol.ErrorAmbiguousResult || callErr.Delivery != DeliverySent {
		t.Fatalf("CaptureTurn() error = %#v, want ambiguous/sent", err)
	}
	if callErr.RequestID == "" || callErr.IdempotencyKey != "capture-sent" || callErr.ReplaySafe {
		t.Fatalf("CallError = %#v", callErr)
	}

	time.Sleep(50 * time.Millisecond)
	if session.State() != StateReady {
		t.Fatalf("State() = %q after late response, want ready", session.State())
	}
	if _, err := session.Health(context.Background()); err != nil {
		t.Fatalf("Health() after late response error = %v", err)
	}
	shutdownFixture(t, session)
}

func TestSessionQueuedCancellationIsNotSent(t *testing.T) {
	session := startFixtureSession(t, "block-stdin-after-init", "token-queued-cancel")
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := session.Recall(firstCtx, CallMeta{}, largeRecall("first"))
		firstDone <- err
	}()
	waitForOutboundState(t, session, "2", outboundWriting)

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := session.Recall(secondCtx, CallMeta{}, largeRecall("second"))
		secondDone <- err
	}()
	waitForOutboundState(t, session, "3", outboundQueued)
	cancelSecond()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Recall() error = %v, want context.Canceled", err)
	}
	session.pendingMu.Lock()
	_, stillPending := session.pending["3"]
	session.pendingMu.Unlock()
	if stillPending {
		t.Fatal("queued canceled request remains pending")
	}

	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("writing Recall() error = %v, want context.Canceled", err)
	}
	waitDone(t, session, 3*time.Second)
}

func TestSessionCaptureCancellationWhileWritingIsAmbiguousMaybeSent(t *testing.T) {
	session := startFixtureSession(t, "block-stdin-after-init", "token-capture-writing")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.CaptureTurn(ctx, CallMeta{IdempotencyKey: "capture-writing"}, largeTurn(strings.Repeat("x", 3500<<10)))
		done <- err
	}()
	waitForOutboundState(t, session, "2", outboundWriting)
	cancel()
	err := <-done
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Delivery != DeliveryMaybeSent || callErr.Code != protocol.ErrorAmbiguousResult {
		t.Fatalf("CaptureTurn() error = %#v, want ambiguous/maybe_sent", err)
	}
	waitDone(t, session, 3*time.Second)
}

func TestSessionInFlightLimitLeavesNoGhostPending(t *testing.T) {
	session := startFixtureSession(t, "block-capture", "token-inflight")
	contexts := make([]context.CancelFunc, 0, 2)
	results := make(chan error, 2)
	for index := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		contexts = append(contexts, cancel)
		go func(index int) {
			_, err := session.CaptureTurn(ctx, CallMeta{IdempotencyKey: "active-" + string(rune('a'+index))}, largeTurn("active"))
			results <- err
		}(index)
	}
	waitForDiagnosticCount(t, session, "CAPTURE_STARTED", 2)

	thirdCtx, cancelThird := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelThird()
	if _, err := session.Health(thirdCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third call error = %v, want deadline exceeded", err)
	}
	if got := pendingCount(session); got != 2 {
		t.Fatalf("pending count = %d, want only two active calls", got)
	}
	for _, cancel := range contexts {
		cancel()
	}
	for range 2 {
		var callErr *CallError
		if err := <-results; !errors.As(err, &callErr) || callErr.Delivery != DeliverySent {
			t.Fatalf("active capture error = %#v", err)
		}
	}
	shutdownFixture(t, session)
}

func TestSessionAllocatesRequestIDsOnlyAfterInFlightSlot(t *testing.T) {
	session := startFixtureSession(t, "block-capture", "token-id-window")
	const callCount = 100
	cancels := make([]context.CancelFunc, 0, callCount)
	results := make(chan error, callCount)
	for index := range callCount {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		go func(index int) {
			_, err := session.CaptureTurn(ctx, CallMeta{IdempotencyKey: "queued-" + string(rune(index+1))}, largeTurn("active"))
			results <- err
		}(index)
	}
	waitForDiagnosticCount(t, session, "CAPTURE_STARTED", 2)
	if issued := session.issued.Load(); issued > 3 {
		t.Fatalf("issued request IDs = %d, want initialize plus at most two in-flight calls", issued)
	}
	for _, cancel := range cancels {
		cancel()
	}
	for range callCount {
		<-results
	}
	shutdownFixture(t, session)
}

func TestDispatchRPCSerializesIDAllocationThroughOutboundEnqueue(t *testing.T) {
	session := &processSession{
		nonce: "dispatch-test", outbound: make(chan *outboundItem, 2),
		pending: make(map[string]*pendingCall), done: make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	firstBuilding := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondBuilding := make(chan struct{})
	dispatched := make(chan error, 2)
	go func() {
		_, _, err := session.dispatchRPC(ctx, "", "health", false, func(meta protocol.RequestMeta) (any, error) {
			close(firstBuilding)
			<-releaseFirst
			return protocol.HealthParams{Meta: meta}, nil
		})
		dispatched <- err
	}()
	<-firstBuilding
	go func() {
		_, _, err := session.dispatchRPC(ctx, "", "health", false, func(meta protocol.RequestMeta) (any, error) {
			close(secondBuilding)
			return protocol.HealthParams{Meta: meta}, nil
		})
		dispatched <- err
	}()
	select {
	case <-secondBuilding:
		t.Fatal("second request allocated an ID before the first request was enqueued")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		if err := <-dispatched; err != nil {
			t.Fatalf("dispatchRPC() error = %v", err)
		}
	}

	for _, wantID := range []string{"1", "2"} {
		item := <-session.outbound
		message, err := protocol.ParseMessage(item.payload)
		if err != nil {
			t.Fatalf("ParseMessage() error = %v", err)
		}
		if message.ID != wantID {
			t.Fatalf("outbound ID = %q, want %q", message.ID, wantID)
		}
	}
}

func TestSessionShutdownEscalatesWhenProviderIgnoresSignal(t *testing.T) {
	session := startFixtureSession(t, "ignore-shutdown-sigterm", "token-shutdown-kill")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := session.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	waitDone(t, session, time.Second)
	event, ok := session.ExitEvent()
	if !ok || !event.Expected || event.ExitCode == 0 {
		t.Fatalf("ExitEvent() = %#v/%v, want expected forced exit", event, ok)
	}
}

func TestQueuedCancelRaceNeverSendsAfterNotSent(t *testing.T) {
	for range 1000 {
		item := &outboundItem{}
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(1)
		writerWon := make(chan bool, 1)
		go func() {
			defer wait.Done()
			<-start
			writerWon <- item.state.CompareAndSwap(uint32(outboundQueued), uint32(outboundWriting))
		}()
		close(start)
		delivery := deliveryForCanceledItem(item)
		wait.Wait()
		won := <-writerWon
		if delivery == DeliveryNotSent && won {
			t.Fatal("reported not_sent after writer transitioned to writing")
		}
	}
}

func largeTurn(content string) model.Turn {
	return model.Turn{
		Identity: testIdentity(true),
		Messages: []model.Message{{Role: "user", Content: content}, {Role: "assistant", Content: "ack"}},
	}
}

func largeRecall(prefix string) model.RecallRequest {
	return model.RecallRequest{Identity: testIdentity(false), Query: prefix + strings.Repeat("x", 3500<<10), MaxItems: 5}
}

func waitForDiagnostic(t *testing.T, session *processSession, marker string) {
	t.Helper()
	waitForDiagnosticCount(t, session, marker, 1)
}

func waitForDiagnosticCount(t *testing.T, session *processSession, marker string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(session.stderr.String(), marker) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("stderr did not contain %d occurrence(s) of %q: %q", count, marker, session.stderr.String())
}

func waitForOutboundState(t *testing.T, session *processSession, id string, state outboundState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.pendingMu.Lock()
		pending := session.pending[id]
		session.pendingMu.Unlock()
		if pending != nil && outboundState(pending.item.state.Load()) == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	session.pendingMu.Lock()
	states := make(map[string]outboundState, len(session.pending))
	for pendingID, pending := range session.pending {
		states[pendingID] = outboundState(pending.item.state.Load())
	}
	session.pendingMu.Unlock()
	event, exited := session.ExitEvent()
	t.Fatalf(
		"request %s did not reach state %d; pending states = %#v, session state = %s, exit = %#v/%v, stderr = %q",
		id, state, states, session.State(), event, exited, session.stderr.String(),
	)
}

func pendingCount(session *processSession) int {
	session.pendingMu.Lock()
	defer session.pendingMu.Unlock()
	return len(session.pending)
}

func shutdownFixture(t *testing.T, session *processSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func waitDone(t *testing.T, session *processSession, timeout time.Duration) {
	t.Helper()
	select {
	case <-session.Done():
	case <-time.After(timeout):
		t.Fatalf("Session did not stop within %s", timeout)
	}
}
