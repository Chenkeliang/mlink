//go:build integration

package tencentdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mlink/internal/model"
)

type liveFixture struct {
	provider *Provider
	identity model.IdentityScope
	canary   string
}

var (
	seededLiveOnce sync.Once
	seededLive     *liveFixture
	seededLiveErr  error
)

func TestLiveNewUserIsEmpty(t *testing.T) {
	provider, identity := newLiveProvider(t, "empty")
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity: identity,
		Query:    "不存在的记忆",
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(bundle.Items) != 0 {
		t.Fatalf("new user received %d item(s): %#v", len(bundle.Items), bundle.Items)
	}
}

func TestLiveCaptureAndEventuallyRecallL1(t *testing.T) {
	fixture := seededLiveFixture(t)
	bundle := eventuallyRecall(t, fixture.provider, model.RecallRequest{
		Identity: fixture.identity,
		Query:    fixture.canary,
	}, 2*time.Minute, func(bundle model.ContextBundle) bool {
		return bundleContains(bundle, model.ScopeUser, fixture.canary)
	})
	if !bundleContains(bundle, model.ScopeUser, fixture.canary) {
		t.Fatalf("L1 recall did not contain canary %q: %#v", fixture.canary, bundle)
	}
}

func TestLiveDuplicateContentProducesNoDuplicateContextID(t *testing.T) {
	fixture := seededLiveFixture(t)
	bundle := eventuallyRecall(t, fixture.provider, model.RecallRequest{
		Identity: fixture.identity,
		Query:    fixture.canary,
		MaxItems: 20,
	}, 2*time.Minute, func(bundle model.ContextBundle) bool {
		return bundleContains(bundle, model.ScopeUser, fixture.canary)
	})
	seen := make(map[string]struct{}, len(bundle.Items))
	for _, item := range bundle.Items {
		if _, exists := seen[item.ID]; exists {
			t.Fatalf("duplicate ContextItem ID %q in %#v", item.ID, bundle.Items)
		}
		seen[item.ID] = struct{}{}
	}
}

func TestLiveTwoUsersDoNotShareL1(t *testing.T) {
	fixture := seededLiveFixture(t)
	other := fixture.identity
	other.UserID += "-other"
	other.SessionID += "-other"
	other.TurnID += "-other"

	bundle, err := fixture.provider.Recall(context.Background(), model.RecallRequest{
		Identity: other,
		Query:    fixture.canary,
		MaxItems: 20,
	})
	if err != nil {
		t.Fatalf("Recall(other user) error = %v", err)
	}
	if bundleContains(bundle, model.ScopeUser, fixture.canary) {
		t.Fatalf("other user received private canary %q: %#v", fixture.canary, bundle.Items)
	}
}

func TestLiveAgentSharedLayersAreOptIn(t *testing.T) {
	fixture := seededLiveFixture(t)
	privateBundle, err := fixture.provider.Recall(context.Background(), model.RecallRequest{
		Identity: fixture.identity,
		Query:    fixture.canary,
		MaxItems: 20,
	})
	if err != nil {
		t.Fatalf("default Recall() error = %v", err)
	}
	for _, item := range privateBundle.Items {
		if item.Scope == model.ScopeAgent {
			t.Fatalf("default recall returned Agent-shared item %#v", item)
		}
	}

	sharedBundle := eventuallyRecall(t, fixture.provider, model.RecallRequest{
		Identity:           fixture.identity,
		Query:              fixture.canary,
		MaxItems:           20,
		IncludeAgentShared: true,
	}, 3*time.Minute, func(bundle model.ContextBundle) bool {
		for _, item := range bundle.Items {
			if item.Scope == model.ScopeAgent {
				return true
			}
		}
		return false
	})
	for _, item := range sharedBundle.Items {
		if item.Scope == model.ScopeAgent {
			return
		}
	}
	t.Fatalf("opt-in recall did not return Agent-shared L2/L3: %#v", sharedBundle)
}

func TestLiveMissingIdentityIsRejectedLocally(t *testing.T) {
	provider, _ := newLiveProvider(t, "missing-identity")
	_, err := provider.Recall(context.Background(), model.RecallRequest{Query: "probe"})
	if !errors.Is(err, model.ErrInvalidIdentity) {
		t.Fatalf("Recall() error = %v, want ErrInvalidIdentity", err)
	}
}

func TestLiveTransientInventoryIsNotPromotedToL1(t *testing.T) {
	provider, identity := newLiveProvider(t, "inventory")
	messages := make([]model.Message, 0, 10)
	for range 5 {
		messages = append(messages,
			model.Message{Role: "user", Content: "当前京东测试 SKU JD-LIVE-001 的库存是 104。"},
			model.Message{Role: "assistant", Content: "JD-LIVE-001 当前库存为 104。"},
		)
	}
	if _, err := provider.CaptureTurn(context.Background(), model.Turn{Identity: identity, Messages: messages}); err != nil {
		t.Fatalf("CaptureTurn() error = %v", err)
	}
	waitForL1Idle(t, provider.client, 2*time.Minute)

	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity: identity,
		Query:    "JD-LIVE-001 库存 104",
		MaxItems: 20,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(bundle.Items) != 0 {
		t.Fatalf("transient inventory was promoted to L1: %#v", bundle.Items)
	}
}

func seededLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	seededLiveOnce.Do(func() {
		provider, identity, err := makeLiveProvider("seeded")
		if err != nil {
			seededLiveErr = err
			return
		}
		canary := "MLINK_LIVE_" + fmt.Sprint(time.Now().UnixNano())
		messages := make([]model.Message, 0, 10)
		for range 5 {
			messages = append(messages,
				model.Message{Role: "user", Content: "我的长期个人代号是 " + canary + "，以后询问个人代号时请使用它。"},
				model.Message{Role: "assistant", Content: "已记录你的长期个人代号 " + canary + "。"},
			)
		}
		if _, err := provider.CaptureTurn(context.Background(), model.Turn{Identity: identity, Messages: messages}); err != nil {
			seededLiveErr = err
			return
		}
		seededLive = &liveFixture{provider: provider, identity: identity, canary: canary}
	})
	if seededLiveErr != nil {
		t.Fatalf("seed live fixture: %v", seededLiveErr)
	}
	return seededLive
}

func newLiveProvider(t *testing.T, suffix string) (*Provider, model.IdentityScope) {
	t.Helper()
	provider, identity, err := makeLiveProvider(suffix)
	if err != nil {
		t.Skip(err)
	}
	return provider, identity
}

func makeLiveProvider(suffix string) (*Provider, model.IdentityScope, error) {
	baseURL := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_URL"))
	token := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_TOKEN"))
	if baseURL == "" || token == "" {
		return nil, model.IdentityScope{}, errors.New("MLINK_TEST_MEMORYCORE_URL and MLINK_TEST_MEMORYCORE_TOKEN are required")
	}
	prefix := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_SERVICE_PREFIX"))
	if prefix == "" {
		prefix = "mlink-live"
	}
	runID := fmt.Sprintf("%s-%s-%d", prefix, suffix, time.Now().UnixNano())
	client, err := NewClient(Config{
		BaseURL:   baseURL,
		Token:     token,
		ServiceID: runID,
	})
	if err != nil {
		return nil, model.IdentityScope{}, err
	}
	identity := model.IdentityScope{
		TenantID:  "team-" + runID,
		UserID:    "user-" + runID,
		AgentID:   "agent-" + runID,
		SessionID: "session-" + runID,
		TurnID:    "turn-" + runID,
	}
	return NewProvider(client), identity, nil
}

func eventuallyRecall(
	t *testing.T,
	provider *Provider,
	request model.RecallRequest,
	timeout time.Duration,
	ready func(model.ContextBundle) bool,
) model.ContextBundle {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last model.ContextBundle
	var lastErr error
	for {
		last, lastErr = provider.Recall(ctx, request)
		if lastErr == nil && ready(last) {
			return last
		}
		select {
		case <-ctx.Done():
			t.Fatalf("memory did not become visible in %s: last_error=%v last_bundle=%#v", timeout, lastErr, last)
		case <-ticker.C:
		}
	}
}

func waitForL1Idle(t *testing.T, client *Client, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	seenBusy := false
	for {
		var status struct {
			L1 struct {
				Queued  int  `json:"queued"`
				Running int  `json:"running"`
				Idle    bool `json:"idle"`
			} `json:"l1"`
		}
		err := client.post(ctx, "/v2/pipeline/status", map[string]any{}, &status)
		if err == nil {
			busy := status.L1.Queued > 0 || status.L1.Running > 0
			seenBusy = seenBusy || busy
			if status.L1.Idle && seenBusy {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("L1 pipeline did not become idle in %s: %v", timeout, err)
		case <-ticker.C:
		}
	}
}

func bundleContains(bundle model.ContextBundle, scope model.ScopeKind, text string) bool {
	for _, item := range bundle.Items {
		if item.Scope == scope && strings.Contains(item.Text, text) {
			return true
		}
	}
	return false
}
