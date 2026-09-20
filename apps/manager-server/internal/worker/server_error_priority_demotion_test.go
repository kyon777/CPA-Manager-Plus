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
			if name := r.URL.Query().Get("name"); name != "" && name != "codex-auth.json" {
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

func TestServerErrorPriorityDemotionWorkerRebalancesAbovePeerMaximum(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files" {
			if r.URL.Query().Get("name") != "" {
				_ = json.NewEncoder(w).Encode([]map[string]any{{
					"id": "runtime-auth-a", "name": "codex-a.json", "auth_index": "auth-a",
					"provider": "codex", "email": "a@example.com", "priority": 150,
				}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-auth-a", "name": "codex-a.json", "auth_index": "auth-a", "provider": "codex", "email": "a@example.com", "priority": 150},
				{"id": "runtime-auth-b", "name": "codex-b.json", "auth_index": "auth-b", "provider": "codex", "email": "b@example.com", "priority": 100},
				{"id": "runtime-auth-c", "name": "codex-c.json", "auth_index": "auth-c", "provider": "codex", "email": "c@example.com", "priority": 100},
			})
			return
		}
		if r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields" {
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Priority  int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode priority patch: %v", err)
			}
			if payload.Name != "runtime-auth-a" || payload.AuthIndex != "auth-a" || payload.Priority != 99 {
				t.Fatalf("priority patch = %#v, want target priority 99 below peer maximum 100", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	event := usage.Event{
		EventHash:        "evt-http-502-rebalance",
		Failed:           true,
		FailStatusCode:   http.StatusBadGateway,
		AuthFileSnapshot: "codex-a.json",
		AuthIndex:        "auth-a",
		AccountSnapshot:  "a@example.com",
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

func TestServerErrorPriorityDemotionCandidateAccepts502503AndQualified429(t *testing.T) {
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

	qualifiedRateLimit := base
	qualifiedRateLimit.FailStatusCode = http.StatusTooManyRequests
	qualifiedRateLimit.FailBody = ` { "detail" : "Rate limit exceeded" } `
	if _, ok := serverErrorPriorityDemotionCandidateFromEvent(qualifiedRateLimit, "http://cpa", "management-key"); !ok {
		t.Fatal("HTTP 429 with the TokenAcquisition rate-limit response must produce a priority-demotion candidate")
	}

	for _, testCase := range []struct {
		name       string
		statusCode int
		failBody   string
	}{
		{name: "429 without body", statusCode: http.StatusTooManyRequests},
		{name: "429 with a different detail", statusCode: http.StatusTooManyRequests, failBody: `{"detail":"Too many requests"}`},
		{name: "429 with non-json body", statusCode: http.StatusTooManyRequests, failBody: "Rate limit exceeded"},
		{name: "500 even with matching body", statusCode: http.StatusInternalServerError, failBody: `{"detail":"Rate limit exceeded"}`},
		{name: "gateway timeout", statusCode: http.StatusGatewayTimeout},
		{name: "network authentication required", statusCode: http.StatusNetworkAuthenticationRequired},
		{name: "out of range", statusCode: 600},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			event := base
			event.FailStatusCode = testCase.statusCode
			event.FailBody = testCase.failBody
			if _, ok := serverErrorPriorityDemotionCandidateFromEvent(event, "http://cpa", "management-key"); ok {
				t.Fatalf("event %#v must not produce a priority-demotion candidate", event)
			}
		})
	}
}

func TestServerErrorPriorityAfterPeerRebalance(t *testing.T) {
	tests := []struct {
		name    string
		current int
		peerMax int
		hasPeer bool
		want    int
	}{
		{name: "higher target jumps below lower peers", current: 150, peerMax: 100, hasPeer: true, want: 99},
		{name: "higher peer keeps one-step demotion", current: 100, peerMax: 150, hasPeer: true, want: 99},
		{name: "equal peer keeps one-step demotion", current: 100, peerMax: 100, hasPeer: true, want: 99},
		{name: "no peers keeps one-step demotion", current: 100, peerMax: 0, hasPeer: false, want: 99},
		{name: "clamps below zero", current: 1, peerMax: 0, hasPeer: true, want: 0},
		{name: "zero remains zero", current: 0, peerMax: 100, hasPeer: true, want: 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := serverErrorPriorityAfterPeerRebalance(testCase.current, testCase.peerMax, testCase.hasPeer); got != testCase.want {
				t.Fatalf("rebalance(%d, %d, %t) = %d, want %d", testCase.current, testCase.peerMax, testCase.hasPeer, got, testCase.want)
			}
		})
	}
}

func TestServerErrorPriorityPeerMaximumFiltersTargetDisabledAndOtherProviders(t *testing.T) {
	target := cpaauthfiles.FromMap(map[string]any{
		"id": "target", "name": "target.json", "auth_index": "target-index", "provider": "codex", "priority": 150,
	})
	files := []cpaauthfiles.File{
		target,
		cpaauthfiles.FromMap(map[string]any{
			"id": "peer", "name": "peer.json", "auth_index": "peer-index", "provider": "codex", "priority": 100,
		}),
		cpaauthfiles.FromMap(map[string]any{
			"id": "disabled-peer", "name": "disabled.json", "auth_index": "disabled-index", "provider": "codex", "priority": 999, "disabled": true,
		}),
		cpaauthfiles.FromMap(map[string]any{
			"id": "other-provider", "name": "other.json", "auth_index": "other-index", "provider": "xai", "priority": 888,
		}),
		cpaauthfiles.FromMap(map[string]any{
			"id": "invalid-peer", "name": "invalid.json", "auth_index": "invalid-index", "provider": "codex", "priority": "not-an-integer",
		}),
	}

	peerMax, hasPeer := serverErrorPriorityPeerMaximum(files, target)
	if !hasPeer || peerMax != 100 {
		t.Fatalf("peer maximum = (%d, %t), want (100, true)", peerMax, hasPeer)
	}
}
