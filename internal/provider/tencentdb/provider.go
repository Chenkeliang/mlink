package tencentdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"mlink/internal/model"
)

type Provider struct {
	client *Client
}

func NewProvider(client *Client) *Provider {
	return &Provider{client: client}
}

func (p *Provider) CaptureTurn(ctx context.Context, turn model.Turn) (model.WriteReceipt, error) {
	if err := turn.Identity.ValidateForCapture(); err != nil {
		return model.WriteReceipt{}, err
	}
	if p == nil || p.client == nil {
		return model.WriteReceipt{}, errors.New("TencentDB provider is not initialized")
	}
	if len(turn.Messages) == 0 {
		return model.WriteReceipt{}, errors.New("capture turn requires at least one message")
	}

	type requestMessage struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp,omitempty"`
	}
	messages := make([]requestMessage, 0, len(turn.Messages))
	for i, message := range turn.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return model.WriteReceipt{}, fmt.Errorf("message %d has unsupported role %q", i, message.Role)
		}
		if strings.TrimSpace(message.Content) == "" {
			return model.WriteReceipt{}, fmt.Errorf("message %d has empty content", i)
		}
		converted := requestMessage{Role: message.Role, Content: message.Content}
		if !message.OccurredAt.IsZero() {
			converted.Timestamp = message.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		messages = append(messages, converted)
	}

	request := struct {
		TeamID    string           `json:"team_id"`
		AgentID   string           `json:"agent_id"`
		UserID    string           `json:"user_id"`
		SessionID string           `json:"session_id"`
		Messages  []requestMessage `json:"messages"`
	}{
		TeamID:    turn.Identity.TenantID,
		AgentID:   turn.Identity.AgentID,
		UserID:    turn.Identity.UserID,
		SessionID: turn.Identity.SessionID,
		Messages:  messages,
	}
	var response struct {
		AcceptedIDs []string `json:"accepted_ids"`
	}
	if err := p.client.post(ctx, "/v3/conversation/add", request, &response); err != nil {
		return model.WriteReceipt{}, err
	}
	return model.WriteReceipt{AcceptedIDs: response.AcceptedIDs}, nil
}

func (p *Provider) Recall(ctx context.Context, request model.RecallRequest) (model.ContextBundle, error) {
	if err := request.Identity.ValidateForRecall(); err != nil {
		return model.ContextBundle{}, err
	}
	if p == nil || p.client == nil {
		return model.ContextBundle{}, errors.New("TencentDB provider is not initialized")
	}
	if strings.TrimSpace(request.Query) == "" {
		return model.ContextBundle{}, errors.New("recall query is empty")
	}

	limit := request.MaxItems
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	l1Items, err := p.recallL1(ctx, request, limit)
	if err != nil {
		return model.ContextBundle{}, err
	}
	bundle := model.ContextBundle{}
	seen := make(map[string]struct{}, limit)
	appendUniqueItems(&bundle, seen, l1Items, limit)
	if !request.IncludeAgentShared || len(bundle.Items) >= limit {
		return bundle, nil
	}

	type sharedResult struct {
		layer string
		items []model.ContextItem
		err   error
	}
	results := make(chan sharedResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		items, err := p.recallL2(ctx, request, limit)
		results <- sharedResult{layer: "L2", items: items, err: err}
	}()
	go func() {
		defer wg.Done()
		items, err := p.recallL3(ctx, request)
		results <- sharedResult{layer: "L3", items: items, err: err}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		if result.err != nil {
			bundle.Partial = true
			bundle.Warnings = append(bundle.Warnings, fmt.Sprintf("%s recall unavailable: %v", result.layer, result.err))
			continue
		}
		appendUniqueItems(&bundle, seen, result.items, limit)
	}
	return bundle, nil
}

func (p *Provider) recallL1(ctx context.Context, request model.RecallRequest, limit int) ([]model.ContextItem, error) {
	body := recallScopeBody(request)
	body["query"] = request.Query
	body["limit"] = limit
	var response struct {
		Items []struct {
			ID        string   `json:"id"`
			Type      string   `json:"type"`
			Content   string   `json:"content"`
			CreatedAt string   `json:"created_at"`
			UpdatedAt string   `json:"updated_at"`
			Score     *float64 `json:"score"`
		} `json:"items"`
	}
	if err := p.client.post(ctx, "/v3/atomic/search", body, &response); err != nil {
		return nil, err
	}
	items := make([]model.ContextItem, 0, len(response.Items))
	for _, item := range response.Items {
		items = append(items, model.ContextItem{
			ID:        "l1:" + item.ID,
			Kind:      item.Type,
			Scope:     model.ScopeUser,
			Text:      item.Content,
			CreatedAt: parseTime(item.CreatedAt),
			UpdatedAt: parseTime(item.UpdatedAt),
			Score:     item.Score,
			Source:    "tencentdb:l1",
		})
	}
	return items, nil
}

func (p *Provider) recallL2(ctx context.Context, request model.RecallRequest, limit int) ([]model.ContextItem, error) {
	body := recallScopeBody(request)
	var listed struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	if err := p.client.post(ctx, "/v3/scenario/ls", body, &listed); err != nil {
		return nil, err
	}
	items := make([]model.ContextItem, 0, len(listed.Entries))
	for _, entry := range listed.Entries {
		if entry.Path == "" || strings.HasSuffix(entry.Path, "/") || len(items) >= limit {
			continue
		}
		readBody := recallScopeBody(request)
		readBody["path"] = entry.Path
		var file struct {
			Content   string `json:"content"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		}
		if err := p.client.post(ctx, "/v3/scenario/read", readBody, &file); err != nil {
			return nil, err
		}
		if strings.TrimSpace(file.Content) == "" {
			continue
		}
		items = append(items, model.ContextItem{
			ID:        "l2:" + entry.Path,
			Kind:      "scenario",
			Scope:     model.ScopeAgent,
			Text:      file.Content,
			CreatedAt: parseTime(file.CreatedAt),
			UpdatedAt: parseTime(file.UpdatedAt),
			Source:    "tencentdb:l2",
		})
	}
	return items, nil
}

func (p *Provider) recallL3(ctx context.Context, request model.RecallRequest) ([]model.ContextItem, error) {
	var file struct {
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := p.client.post(ctx, "/v3/core/read", recallScopeBody(request), &file); err != nil {
		return nil, err
	}
	if strings.TrimSpace(file.Content) == "" {
		return nil, nil
	}
	return []model.ContextItem{{
		ID:        "l3:persona",
		Kind:      "profile",
		Scope:     model.ScopeAgent,
		Text:      file.Content,
		CreatedAt: parseTime(file.CreatedAt),
		UpdatedAt: parseTime(file.UpdatedAt),
		Source:    "tencentdb:l3",
	}}, nil
}

func recallScopeBody(request model.RecallRequest) map[string]any {
	body := map[string]any{
		"team_id":  request.Identity.TenantID,
		"agent_id": request.Identity.AgentID,
		"user_id":  request.Identity.UserID,
	}
	if request.Identity.SessionID != "" {
		body["session_id"] = request.Identity.SessionID
	}
	return body
}

func appendUniqueItems(bundle *model.ContextBundle, seen map[string]struct{}, items []model.ContextItem, limit int) {
	for _, item := range items {
		if len(bundle.Items) >= limit {
			return
		}
		if _, exists := seen[item.ID]; exists {
			continue
		}
		seen[item.ID] = struct{}{}
		bundle.Items = append(bundle.Items, item)
	}
}

func parseTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}
