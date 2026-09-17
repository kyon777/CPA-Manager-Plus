package cpaauthfiles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPatchPriorityTargetUsesVerifiedRuntimeIdentityAndClampsToZero(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v0/management/auth-files/fields" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer management-key" {
			t.Fatalf("authorization = %q", got)
		}
		var payload struct {
			Name      string `json:"name"`
			AuthIndex string `json:"auth_index"`
			Priority  int    `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode priority payload: %v", err)
		}
		if payload.Name != "runtime-auth-1" || payload.AuthIndex != "auth-1" || payload.Priority != 0 {
			t.Fatalf("priority payload = %#v", payload)
		}
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	target := StatusMutationTarget{
		Selector: "runtime-auth-1",
		File: File{
			ID:        "runtime-auth-1",
			Name:      "codex-auth.json",
			AuthIndex: "auth-1",
		},
		Scope: StatusMutationScopeCredential,
	}
	if err := New(server.Client()).PatchPriorityTarget(
		context.Background(),
		server.URL,
		"management-key",
		target,
		-99,
	); err != nil {
		t.Fatalf("PatchPriorityTarget() error = %v", err)
	}
	if !called {
		t.Fatal("priority patch was not sent")
	}
}
