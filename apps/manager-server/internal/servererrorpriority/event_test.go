package servererrorpriority

import (
	"net/http"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestClassifyAcceptsOnlyApprovedErrorEvents(t *testing.T) {
	base := usage.Event{
		Failed:           true,
		EventHash:        "evt",
		TimestampMS:      123,
		AuthFileSnapshot: "a.json",
		AuthIndex:        "idx",
		Provider:         "codex",
		AccountSnapshot:  "a@example.com",
	}
	cases := []struct {
		name string
		code int
		body string
		want string
	}{
		{name: "502", code: http.StatusBadGateway, want: QualificationHTTP502},
		{name: "503", code: http.StatusServiceUnavailable, want: QualificationHTTP503},
		{name: "429", code: http.StatusTooManyRequests, body: `{"detail":"Rate limit exceeded"}`, want: QualificationRateLimit429},
		{name: "500 eof", code: http.StatusInternalServerError, body: `Post "https://chatgpt.com/backend-api/codex/responses": unexpected EOF`, want: QualificationCodexTransport500},
		{name: "500 proxy authentication", code: http.StatusInternalServerError, body: `Post "https://chatgpt.com/backend-api/codex/responses": utls: dial upstream: proxy CONNECT returned status 407 Proxy Authentication Required`, want: QualificationCodexTransport500},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event := base
			event.FailStatusCode = testCase.code
			event.FailBody = testCase.body
			got, ok := Classify(event)
			if !ok {
				t.Fatal("Classify() did not accept eligible event")
			}
			if got.QualificationCode != testCase.want {
				t.Fatalf("qualification = %q, want %q", got.QualificationCode, testCase.want)
			}
			if got.CredentialKey == "" {
				t.Fatal("credential key is empty")
			}
		})
	}
}

func TestClassifyRejectsGeneric429And500(t *testing.T) {
	base := usage.Event{
		Failed:           true,
		AuthFileSnapshot: "a.json",
		AuthIndex:        "idx",
		Provider:         "codex",
	}
	cases := []usage.Event{
		withFailure(base, http.StatusTooManyRequests, ""),
		withFailure(base, http.StatusTooManyRequests, `{"detail":"different"}`),
		withFailure(base, http.StatusTooManyRequests, "Rate limit exceeded"),
		withFailure(base, http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/conversations": unexpected EOF`),
		withFailure(base, http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/codex/responses": connection reset by peer`),
		withFailure(base, http.StatusInternalServerError, `Post "https://chatgpt.com/backend-api/codex/responses": proxy CONNECT returned status 407 Proxy Authentication Required`),
	}
	for _, event := range cases {
		if _, ok := Classify(event); ok {
			t.Fatalf("unexpected eligible event: %#v", event)
		}
	}
}

func TestClassifyMarksEligibleEventWithoutStableCredentialIdentityIgnored(t *testing.T) {
	input, ok := Classify(usage.Event{
		Failed:         true,
		FailStatusCode: http.StatusBadGateway,
		EventHash:      "evt-no-identity",
	})
	if !ok {
		t.Fatal("qualified event was not classified")
	}
	if input.IgnoredReason != IgnoredReasonCredentialIdentityMissing {
		t.Fatalf("ignored reason = %q, want %q", input.IgnoredReason, IgnoredReasonCredentialIdentityMissing)
	}
	if input.CredentialKey != "" {
		t.Fatalf("credential key = %q, want empty", input.CredentialKey)
	}
}

func TestClassifyUsesAuthProviderSnapshotAndStripsFileNameAsAccountSnapshot(t *testing.T) {
	input, ok := Classify(usage.Event{
		Failed:                true,
		FailStatusCode:        http.StatusBadGateway,
		EventHash:             "evt-snapshot",
		AuthFileSnapshot:      "account.json",
		AuthIndex:             "auth-index",
		Provider:              "ignored-provider",
		AuthProviderSnapshot:  "CodeX",
		AccountSnapshot:       "account.json",
		AuthAccountIDSnapshot: "workspace-id",
	})
	if !ok {
		t.Fatal("qualified event was not classified")
	}
	if input.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", input.Provider)
	}
	if input.AccountSnapshot != "" {
		t.Fatalf("account snapshot = %q, want empty", input.AccountSnapshot)
	}
	if input.AccountIDSnapshot != "workspace-id" {
		t.Fatalf("account ID snapshot = %q", input.AccountIDSnapshot)
	}
}

func withFailure(event usage.Event, statusCode int, body string) usage.Event {
	event.FailStatusCode = statusCode
	event.FailBody = body
	return event
}
