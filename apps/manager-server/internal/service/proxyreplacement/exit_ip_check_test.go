package proxyreplacement

import (
	"context"
	"errors"
	"net/url"
	"syscall"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestExitIPCheckerReturnsHealthyForValidIPv4(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{
		StatusCode: 200,
		Body:       []byte(`{"ip":"203.0.113.7"}`),
	}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://user:secret@proxy.example:8080/")
	if got.Outcome != model.ProxyExitIPOutcomeHealthy {
		t.Fatalf("outcome = %q", got.Outcome)
	}
	if got.ExitIP != "203.0.113.7" {
		t.Fatalf("exit IP = %q", got.ExitIP)
	}
	if got.Reason != "" {
		t.Fatalf("healthy result reason = %q", got.Reason)
	}
	if got.LatencyMS < 0 {
		t.Fatalf("latency = %d", got.LatencyMS)
	}
	if len(fetcher.requests) != 1 || fetcher.requests[0].target != "https://echo.example.test/ip" {
		t.Fatalf("unexpected requests = %#v", fetcher.requests)
	}
}

func TestExitIPCheckerClassifies407AsProxyFailure(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{StatusCode: 407}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeProxyFailed || got.Reason != "proxy_auth_407" {
		t.Fatalf("result = %#v", got)
	}
	if len(fetcher.requests) != 1 {
		t.Fatalf("407 must not retry, calls = %d", len(fetcher.requests))
	}
}

func TestExitIPCheckerLeavesEcho429Inconclusive(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{StatusCode: 429}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeInconclusive || got.Reason != "ip_echo_http_429" {
		t.Fatalf("result = %#v", got)
	}
}

func TestExitIPCheckerLeavesEcho5xxInconclusive(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{StatusCode: 503}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeInconclusive || got.Reason != "ip_echo_http_5xx" {
		t.Fatalf("result = %#v", got)
	}
}

func TestExitIPCheckerRetriesRetryableTransportFailureOnce(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{
		{Err: timeoutError{}},
		{StatusCode: 200, Body: []byte(`{"ip":"2001:db8::8"}`)},
	}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeHealthy || got.ExitIP != "2001:db8::8" {
		t.Fatalf("result = %#v", got)
	}
	if len(fetcher.requests) != 2 {
		t.Fatalf("retry calls = %d, want 2", len(fetcher.requests))
	}
}

func TestExitIPCheckerMarksTimeoutAfterRetryAsProxyFailure(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{
		{Err: timeoutError{}},
		{Err: timeoutError{}},
	}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeProxyFailed || got.Reason != "proxy_connect_timeout" {
		t.Fatalf("result = %#v", got)
	}
	if len(fetcher.requests) != 2 {
		t.Fatalf("retry calls = %d, want 2", len(fetcher.requests))
	}
}

func TestExitIPCheckerClassifiesConnectionRefusedAsProxyFailure(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{
		{Err: syscall.ECONNREFUSED},
		{Err: syscall.ECONNREFUSED},
	}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeProxyFailed || got.Reason != "proxy_connect_refused" {
		t.Fatalf("result = %#v", got)
	}
}

func TestExitIPCheckerLeavesMalformedEchoResponseInconclusive(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{
		StatusCode: 200,
		Body:       []byte(`{"ip":"not-an-ip"}`),
	}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeInconclusive || got.Reason != "ip_echo_invalid_response" {
		t.Fatalf("result = %#v", got)
	}
}

func TestExitIPCheckerLeavesOversizedEchoResponseInconclusive(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{
		StatusCode:   200,
		Body:         []byte(`{"ip":"203.0.113.7"}`),
		BodyTooLarge: true,
	}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "http://proxy.example:8080")
	if got.Outcome != model.ProxyExitIPOutcomeInconclusive || got.Reason != "ip_echo_invalid_response" {
		t.Fatalf("result = %#v", got)
	}
}

func TestExitIPCheckerRejectsInvalidProxyWithoutNetworkRequest(t *testing.T) {
	fetcher := &scriptedExitIPFetcher{}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), "not a proxy URL")
	if got.Outcome != model.ProxyExitIPOutcomeProxyFailed || got.Reason != "proxy_url_invalid" {
		t.Fatalf("result = %#v", got)
	}
	if len(fetcher.requests) != 0 {
		t.Fatalf("invalid URL started request: %#v", fetcher.requests)
	}
}

func TestExitIPCheckerDoesNotExposeProxyURLInResult(t *testing.T) {
	proxyURL := "http://user:very-secret@proxy.example:8080"
	fetcher := &scriptedExitIPFetcher{results: []exitIPFetchResult{{Err: errors.New("dial " + proxyURL + ": refused")}}}
	checker := newExitIPCheckerForTest(t, fetcher)

	got := checker.Check(context.Background(), proxyURL)
	if got.Outcome != model.ProxyExitIPOutcomeProxyFailed {
		t.Fatalf("result = %#v", got)
	}
	if containsString(got.Reason, "secret") || containsString(got.ExitIP, "secret") {
		t.Fatalf("proxy secret leaked in result: %#v", got)
	}
}

func newExitIPCheckerForTest(t *testing.T, fetcher exitIPFetcher) *exitIPChecker {
	t.Helper()
	checker, err := newExitIPChecker("https://echo.example.test/ip", fetcher)
	if err != nil {
		t.Fatalf("new checker: %v", err)
	}
	checker.attemptTimeout = 5 * time.Millisecond
	return checker
}

type exitIPRequest struct {
	proxy  string
	target string
}

type scriptedExitIPFetcher struct {
	results  []exitIPFetchResult
	requests []exitIPRequest
}

func (f *scriptedExitIPFetcher) Fetch(_ context.Context, proxyURL, targetURL *url.URL) exitIPFetchResult {
	f.requests = append(f.requests, exitIPRequest{proxy: proxyURL.String(), target: targetURL.String()})
	if len(f.results) == 0 {
		return exitIPFetchResult{Err: errors.New("missing scripted result")}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func containsString(value, fragment string) bool {
	return len(fragment) > 0 && len(value) >= len(fragment) && containsSubstring(value, fragment)
}

func containsSubstring(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
