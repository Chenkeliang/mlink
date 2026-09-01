package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mlink/internal/broker"
	"mlink/internal/model"
)

const maxResponseBytes = 1 << 20

type Client struct {
	SocketPath     string
	BaseURL        string
	AdapterID      string
	Token          string
	RecallTimeout  time.Duration
	CaptureTimeout time.Duration
	HTTPClient     *http.Client
}

func (c Client) Recall(ctx context.Context, input broker.RecallInput) (model.ContextBundle, error) {
	input.AdapterID = c.AdapterID
	requestCtx := ctx
	cancel := func() {}
	if c.RecallTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.RecallTimeout)
	}
	defer cancel()
	var bundle model.ContextBundle
	err := c.postJSON(requestCtx, "/v1/recall", input, &bundle)
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || timeoutError(err)) {
		return model.ContextBundle{Warnings: []string{"MLink recall timed out; continuing without external memory"}}, nil
	}
	if err != nil {
		return model.ContextBundle{Warnings: []string{"MLink recall unavailable; continuing without external memory"}}, nil
	}
	return bundle, err
}

func (c Client) RecallStrict(ctx context.Context, input broker.RecallInput) (model.ContextBundle, error) {
	input.AdapterID = c.AdapterID
	requestCtx := ctx
	cancel := func() {}
	if c.RecallTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.RecallTimeout)
	}
	defer cancel()
	var bundle model.ContextBundle
	if err := c.postJSON(requestCtx, "/v1/recall", input, &bundle); err != nil {
		return model.ContextBundle{}, err
	}
	return bundle, nil
}

func (c Client) Status(ctx context.Context) error {
	requestCtx := ctx
	cancel := func() {}
	if c.RecallTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.RecallTimeout)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(c.baseURL(), "/")+"/v1/health?adapter_id="+url.QueryEscape(c.AdapterID), nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Broker health failed with status %d", response.StatusCode)
	}
	return nil
}

func (c Client) SubmitTurn(ctx context.Context, input broker.TurnInput) (broker.SubmitReceipt, error) {
	input.AdapterID = c.AdapterID
	requestCtx, cancel := c.captureContext(ctx)
	defer cancel()
	var receipt broker.SubmitReceipt
	err := c.postJSON(requestCtx, "/v1/turns", input, &receipt)
	return receipt, err
}

func (c Client) SubmitFragment(ctx context.Context, input broker.FragmentInput) error {
	input.AdapterID = c.AdapterID
	requestCtx, cancel := c.captureContext(ctx)
	defer cancel()
	var response map[string]any
	return c.postJSON(requestCtx, "/v1/turn-fragments", input, &response)
}

func (c Client) Flush(ctx context.Context, sessionID string, input broker.FlushInput) (int, error) {
	input.AdapterID = c.AdapterID
	requestCtx, cancel := c.captureContext(ctx)
	defer cancel()
	var response struct {
		Pending int `json:"pending"`
	}
	err := c.postJSON(requestCtx, "/v1/sessions/"+sessionID+"/flush", input, &response)
	return response.Pending, err
}

func (c Client) captureContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.CaptureTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.CaptureTimeout)
}

func (c Client) postJSON(ctx context.Context, path string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode Broker request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL(), "/")+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create Broker request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Broker response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return errors.New("Broker response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Broker request failed with status %d", response.StatusCode)
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode Broker response: %w", err)
	}
	return nil
}

func (c Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	if c.SocketPath == "" {
		return http.DefaultClient
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
	}}
	return &http.Client{Transport: transport}
}

func (c Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "http://mlink"
}

func timeoutError(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
