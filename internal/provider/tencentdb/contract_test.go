package tencentdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"mlink/internal/provider/contract"
)

func TestTencentDBProviderContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) contract.Provider {
		t.Helper()
		var mu sync.Mutex
		memories := make(map[string]string)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v3/conversation/add":
				var body struct {
					UserID   string `json:"user_id"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode capture: %v", err)
					return
				}
				mu.Lock()
				memories[body.UserID] = body.Messages[0].Content
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-write","data":{"accepted_ids":["msg-a"]}}`))
			case "/v3/atomic/search":
				var body struct {
					UserID string `json:"user_id"`
					Query  string `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode recall: %v", err)
					return
				}
				if body.Query == "duplicate" {
					_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-read","data":{"items":[{"id":"mem-duplicate","type":"instruction","content":"duplicate"},{"id":"mem-duplicate","type":"instruction","content":"duplicate"}]}}`))
					return
				}
				mu.Lock()
				content := memories[body.UserID]
				mu.Unlock()
				if content == "" {
					_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-read","data":{"items":[]}}`))
					return
				}
				_, _ = fmt.Fprintf(w, `{"code":0,"message":"ok","request_id":"req-read","data":{"items":[{"id":"mem-user","type":"episodic","content":%q}]}}`, content)
			default:
				t.Errorf("unexpected contract path %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)

		client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token", ServiceID: "service-contract"})
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		return NewProvider(client)
	})
}
