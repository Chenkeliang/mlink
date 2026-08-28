package tencentdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

type Config struct {
	BaseURL    string
	Token      string
	ServiceID  string
	HTTPClient *http.Client
}

type Client struct {
	baseURL   string
	token     string
	serviceID string
	http      *http.Client
}

type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tencentdb memory API error: http_status=%d code=%d message=%s", e.HTTPStatus, e.Code, e.Message)
}

func NewClient(config Config) (*Client, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid TencentDB MemoryCore base URL")
	}
	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, errors.New("missing TencentDB MemoryCore token")
	}
	serviceID := strings.TrimSpace(config.ServiceID)
	if serviceID == "" {
		return nil, errors.New("missing TencentDB MemoryCore service ID")
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Second}
	}

	return &Client{
		baseURL:   strings.TrimRight(parsed.String(), "/"),
		token:     token,
		serviceID: serviceID,
		http:      httpClient,
	}, nil
}

func (c *Client) post(ctx context.Context, path string, requestBody, responseData any) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode TencentDB MemoryCore request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create TencentDB MemoryCore request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-tdai-service-id", c.serviceID)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call TencentDB MemoryCore: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return safeAPIError(resp.StatusCode, 0)
		}
		return fmt.Errorf("read TencentDB MemoryCore response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return safeAPIError(resp.StatusCode, 0)
		}
		return errors.New("TencentDB MemoryCore response exceeds 4 MiB limit")
	}

	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	decodeErr := json.Unmarshal(raw, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := 0
		if decodeErr == nil && envelope.Code != nil {
			code = *envelope.Code
		}
		return safeAPIError(resp.StatusCode, code)
	}
	if decodeErr != nil {
		return errors.New("TencentDB MemoryCore returned invalid JSON")
	}
	if envelope.Code == nil {
		return errors.New("TencentDB MemoryCore returned invalid response envelope")
	}
	if *envelope.Code != 0 {
		return safeAPIError(resp.StatusCode, *envelope.Code)
	}
	if responseData == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, responseData); err != nil {
		return errors.New("TencentDB MemoryCore returned invalid response data")
	}
	return nil
}

func (c *Client) health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return errors.New("create TencentDB MemoryCore health request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("x-tdai-service-id", c.serviceID)
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("call TencentDB MemoryCore health endpoint")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return safeAPIError(resp.StatusCode, 0)
	}
	return nil
}

func safeAPIError(httpStatus, code int) *APIError {
	message := http.StatusText(httpStatus)
	if httpStatus >= 200 && httpStatus < 300 {
		message = "request rejected"
	}
	return &APIError{HTTPStatus: httpStatus, Code: code, Message: message}
}
