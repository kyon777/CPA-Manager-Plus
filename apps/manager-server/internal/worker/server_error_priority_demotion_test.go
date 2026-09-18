package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestServerErrorPriorityDemotionWorkerDecreasesCurrentPriorityForHTTP502(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			if r.URL.Query().Get("name") != "codex-auth.json" {
				t.Fatalf("auth-file lookup query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-auth-1", "name": "codex-auth.json", "auth_index": "auth-1",
				"provider": "codex", "email": "user@example.com", "priority": 20_000,
			}})
		case "PATCH /v0/management/auth-files/fields":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Priority  int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode priority patch: %v", err)
			}
			if payload.Name != "runtime-auth-1" || payload.AuthIndex != "auth-1" || payload.Priority != 19_999 {
				t.Fatalf("priority patch = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	event := usage.Event{
		EventHash:        "evt-http-502",
		Failed:           true,
		FailStatusCode:   http.StatusBadGateway,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
	}
	candidate, ok := serverErrorPriorityDemotionCandidateFromEvent(event, server.URL, "management-key")
	if !ok {
		t.Fatal("HTTP 502 should produce a priority-demotion candidate")
	}

	worker := NewServerErrorPriorityDemotionWorkerWithMutationCoordinator(
		cpaauthfiles.NewMutationCoordinator(),
	)
	worker.client = server.Client()
	worker.handleCandidate(context.Background(), candidate)

	if patchCalls != 1 {
		t.Fatalf("priority patch calls = %d, want 1", patchCalls)
	}
}

func TestServerErrorPriorityDemotionWorkerDoesNotMakePriorityNegative(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-auth-1", "name": "codex-auth.json", "auth_index": "auth-1",
				"provider": "codex", "email": "user@example.com", "priority": 0,
			}})
		case "PATCH /v0/management/auth-files/fields":
			patchCalls++
			t.Fatal("priority zero must not be patched below zero")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	event := usage.Event{
		EventHash:        "evt-http-503",
		Failed:           true,
		FailStatusCode:   http.StatusServiceUnavailable,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
	}
	candidate, ok := serverErrorPriorityDemotionCandidateFromEvent(event, server.URL, "management-key")
	if !ok {
		t.Fatal("HTTP 503 should produce a priority-demotion candidate")
	}

	worker := NewServerErrorPriorityDemotionWorkerWithMutationCoordinator(
		cpaauthfiles.NewMutationCoordinator(),
	)
	worker.client = server.Client()
	worker.handleCandidate(context.Background(), candidate)

	if patchCalls != 0 {
		t.Fatalf("priority patch calls = %d, want 0", patchCalls)
	}
}

func TestServerErrorPriorityDemotionCandidateOnlyAcceptsHTTP502And503(t *testing.T) {
	base := usage.Event{
		Failed:           true,
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		Provider:         "codex",
	}
	for _, statusCode := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		event := base
		event.FailStatusCode = statusCode
		if _, ok := serverErrorPriorityDemotionCandidateFromEvent(event, "http://cpa", "management-key"); !ok {
			t.Fatalf("HTTP %d should produce a priority-demotion candidate", statusCode)
		}
	}
	for _, statusCode := range []int{
		http.StatusTooManyRequests,
		499,
		http.StatusInternalServerError,
		http.StatusNotImplemented,
		http.StatusGatewayTimeout,
		http.StatusNetworkAuthenticationRequired,
		600,
	} {
		event := base
		event.FailStatusCode = statusCode
		if _, ok := serverErrorPriorityDemotionCandidateFromEvent(event, "http://cpa", "management-key"); ok {
			t.Fatalf("HTTP %d must not produce a priority-demotion candidate", statusCode)
		}
	}
}
