package tokenacquisition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquirePostsHTTPProxyAndPollsUntilDone(t *testing.T) {
	var postCount atomic.Int32
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-admin-key" {
			t.Fatalf("X-Api-Key = %q", r.Header.Get("X-Api-Key"))
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-CDK") != "" {
			t.Fatalf("unexpected client authentication headers: %#v", r.Header)
		}
		switch r.URL.Path {
		case "/v1/tokens":
			postCount.Add(1)
			var request struct {
				Accounts []struct {
					Email string `json:"email"`
					Proxy string `json:"proxy"`
				} `json:"accounts"`
				Direct bool `json:"direct"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.Direct || len(request.Accounts) != 1 || request.Accounts[0].Email != "person@example.com" || request.Accounts[0].Proxy != "http://user:pass@proxy.test:8080" {
				t.Fatalf("request = %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"batch_id": "batch-1"})
		case "/v1/batches/batch-1":
			if pollCount.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"done": false})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"done": true,
				"jobs": []any{map[string]any{
					"email": "person@example.com", "status": "ok",
					"result": map[string]any{
						"email": "person@example.com", "access_token": "new-access", "refresh_token": "new-refresh", "id_token": "new-id-token", "chatgpt_account_id": "new-account-id",
					},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, APIKey: "test-admin-key", HTTPClient: server.Client(), PollInterval: time.Millisecond, Timeout: time.Second})
	result, err := client.Acquire(context.Background(), Request{Email: "person@example.com", HTTPProxy: "http://user:pass@proxy.test:8080"})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if result.AccessToken != "new-access" || result.RefreshToken != "new-refresh" || result.IDToken != "new-id-token" || result.ChatGPTAccountID != "new-account-id" {
		t.Fatalf("result = %#v", result)
	}
	if postCount.Load() != 1 || pollCount.Load() != 2 {
		t.Fatalf("request counts = post:%d poll:%d", postCount.Load(), pollCount.Load())
	}
}

func TestAcquireUsesDirectForCredentialWithoutHTTPProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tokens":
			var request struct {
				Accounts []map[string]any `json:"accounts"`
				Direct   bool             `json:"direct"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if !request.Direct || len(request.Accounts) != 1 || request.Accounts[0]["proxy"] != nil {
				t.Fatalf("direct request = %#v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"batch_id": "batch-2"})
		case "/v1/batches/batch-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "jobs": []any{map[string]any{
				"email": "person@example.com", "status": "ok", "result": map[string]any{
					"email": "person@example.com", "access_token": "access", "refresh_token": "refresh", "id_token": "id",
				},
			}}})
		}
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, APIKey: "test-admin-key", HTTPClient: server.Client(), PollInterval: time.Millisecond, Timeout: time.Second}).Acquire(context.Background(), Request{Email: "person@example.com"})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
}

func TestAcquireRejectsMismatchedOrIncompleteDoneResult(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result map[string]any
		want   error
	}{
		{
			name:   "mismatched email",
			result: map[string]any{"email": "other@example.com", "access_token": "access", "refresh_token": "refresh", "id_token": "id"},
			want:   ErrInvalidResult,
		},
		{
			name:   "incomplete token",
			result: map[string]any{"email": "person@example.com", "access_token": "access", "refresh_token": "", "id_token": "id"},
			want:   ErrInvalidResult,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/tokens":
					_ = json.NewEncoder(w).Encode(map[string]any{"batch_id": "batch-3"})
				case "/v1/batches/batch-3":
					_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "jobs": []any{map[string]any{
						"email": "person@example.com", "status": "ok", "result": testCase.result,
					}}})
				}
			}))
			defer server.Close()
			_, err := New(Config{BaseURL: server.URL, APIKey: "test-admin-key", HTTPClient: server.Client(), PollInterval: time.Millisecond, Timeout: time.Second}).Acquire(context.Background(), Request{Email: "person@example.com"})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Acquire() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestAcquireReturnsConflictWithoutRetryingPOST(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":{"code":"in_progress"}}`))
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, APIKey: "test-admin-key", HTTPClient: server.Client(), Timeout: time.Second}).Acquire(context.Background(), Request{Email: "person@example.com"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Acquire() error = %v, want ErrConflict", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("posts = %d, want 1", posts.Load())
	}
}
