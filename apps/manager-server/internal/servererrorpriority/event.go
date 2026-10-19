// Package servererrorpriority owns the pure, shared interpretation of usage
// events that may lead to a server-error priority demotion. It deliberately
// returns only redacted locator fields; callers must resolve current CPA data
// at execution time rather than persisting credentials or transport secrets.
package servererrorpriority

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	QualificationHTTP502           = "http_502"
	QualificationHTTP503           = "http_503"
	QualificationRateLimit429      = "rate_limit_429"
	QualificationCodexTransport500 = "codex_transport_500"

	IgnoredReasonCredentialIdentityMissing = "credential_identity_missing"
)

// Input is the complete non-secret information needed to admit an eligible
// event into CPAM's durable priority-demotion queue. An event can be eligible
// while being unexecutable: in that case IgnoredReason explains why it must be
// retained as an ignored terminal job rather than sent to CPA.
type Input struct {
	EventHash         string
	FileName          string
	AuthIndex         string
	Provider          string
	AccountSnapshot   string
	AccountIDSnapshot string
	CredentialKey     string
	QualificationCode string
	IgnoredReason     string
	StatusCode        int
	EventAtMS         int64
}

// Classify applies the single approved eligibility rule set. It returns false
// only when an event is not a qualifying server error. A qualifying event
// lacking a stable credential locator returns true with IgnoredReason set so a
// durable queue can audibly retain the reason without making a CPA mutation.
func Classify(event usage.Event) (Input, bool) {
	qualificationCode, ok := qualificationFor(event)
	if !ok {
		return Input{}, false
	}

	fileName := strings.TrimSpace(event.AuthFileSnapshot)
	input := Input{
		EventHash:         strings.TrimSpace(event.EventHash),
		FileName:          fileName,
		AuthIndex:         strings.TrimSpace(event.AuthIndex),
		Provider:          credentialpolicy.NormalizeProvider(firstNonEmpty(event.AuthProviderSnapshot, event.Provider)),
		AccountSnapshot:   stableAccountSnapshot(fileName, event.AccountSnapshot),
		AccountIDSnapshot: strings.TrimSpace(event.AuthAccountIDSnapshot),
		QualificationCode: qualificationCode,
		StatusCode:        event.FailStatusCode,
		EventAtMS:         eventTimeMS(event),
	}
	if input.FileName == "" || input.Provider == "" ||
		(input.Provider == "codex" && input.AuthIndex == "") ||
		(input.AuthIndex == "" && input.AccountIDSnapshot == "" && input.AccountSnapshot == "") {
		input.IgnoredReason = IgnoredReasonCredentialIdentityMissing
		return input, true
	}
	input.CredentialKey = credentialKey(input)
	return input, true
}

// IsEligible reports whether event matches an approved status/body rule. It
// intentionally does not require a usable credential locator.
func IsEligible(event usage.Event) bool {
	_, ok := qualificationFor(event)
	return ok
}

func qualificationFor(event usage.Event) (string, bool) {
	if !event.Failed {
		return "", false
	}
	switch event.FailStatusCode {
	case http.StatusBadGateway:
		return QualificationHTTP502, true
	case http.StatusServiceUnavailable:
		return QualificationHTTP503, true
	case http.StatusTooManyRequests:
		if isQualifiedRateLimit429(event.FailBody) {
			return QualificationRateLimit429, true
		}
	case http.StatusInternalServerError:
		if isQualifiedCodexResponsesTransportFailure500(event.FailBody) {
			return QualificationCodexTransport500, true
		}
	}
	return "", false
}

func isQualifiedRateLimit429(failBody string) bool {
	var response struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(failBody)), &response); err != nil {
		return false
	}
	return response.Detail == "Rate limit exceeded"
}

func isQualifiedCodexResponsesTransportFailure500(failBody string) bool {
	normalized := strings.ToLower(strings.TrimSpace(failBody))
	if !strings.Contains(normalized, `post "https://chatgpt.com/backend-api/codex/responses":`) {
		return false
	}
	return strings.Contains(normalized, "unexpected eof") ||
		strings.Contains(normalized, "utls: dial upstream: proxy connect returned status 407 proxy authentication required")
}

func credentialKey(input Input) string {
	stableIdentity := input.AuthIndex
	if stableIdentity == "" {
		stableIdentity = firstNonEmpty(input.AccountIDSnapshot, input.AccountSnapshot)
	}
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(input.Provider),
		strings.TrimSpace(input.FileName),
		strings.TrimSpace(stableIdentity),
	}, "\x1f"))
}

func stableAccountSnapshot(fileName, account string) string {
	account = strings.TrimSpace(account)
	if account == "" || account == strings.TrimSpace(fileName) {
		return ""
	}
	return account
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func eventTimeMS(event usage.Event) int64 {
	if event.TimestampMS > 0 {
		return event.TimestampMS
	}
	return event.CreatedAtMS
}
