package proxyreplacement

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	defaultExitIPCheckTargetURL = "https://api.ipify.org?format=json"
	exitIPCheckAttempts         = 2
	exitIPCheckAttemptTimeout   = 5 * time.Second
	exitIPCheckResponseLimit    = 4 << 10
)

// ExitIPChecker performs a bounded direct request through a single saved
// proxy. It never receives account credentials or an arbitrary browser target.
type ExitIPChecker interface {
	Check(context.Context, string) model.ProxyExitIPCheckResult
}

type exitIPFetchResult struct {
	StatusCode   int
	Body         []byte
	BodyTooLarge bool
	Err          error
}

// exitIPFetcher is deliberately tiny so checker behavior is deterministic in
// tests. The production implementation constructs a fresh transport for each
// proxy and never inherits an ambient process proxy configuration.
type exitIPFetcher interface {
	Fetch(context.Context, *url.URL, *url.URL) exitIPFetchResult
}

type exitIPChecker struct {
	targetURL      *url.URL
	fetcher        exitIPFetcher
	attempts       int
	attemptTimeout time.Duration
	now            func() time.Time
}

func newExitIPChecker(rawTargetURL string, fetcher exitIPFetcher) (*exitIPChecker, error) {
	targetURL, err := parseExitIPTargetURL(rawTargetURL)
	if err != nil {
		return nil, err
	}
	if fetcher == nil {
		fetcher = httpExitIPFetcher{}
	}
	return &exitIPChecker{
		targetURL:      targetURL,
		fetcher:        fetcher,
		attempts:       exitIPCheckAttempts,
		attemptTimeout: exitIPCheckAttemptTimeout,
		now:            time.Now,
	}, nil
}

// NewExitIPChecker constructs the production verifier. Configuration loading
// validates the target first; if a caller bypasses that path, fail closed with
// an inconclusive result rather than attempting an arbitrary request.
func NewExitIPChecker(rawTargetURL string) ExitIPChecker {
	if strings.TrimSpace(rawTargetURL) == "" {
		rawTargetURL = defaultExitIPCheckTargetURL
	}
	checker, err := newExitIPChecker(rawTargetURL, nil)
	if err != nil {
		return unavailableExitIPChecker{}
	}
	return checker
}

type unavailableExitIPChecker struct{}

func (unavailableExitIPChecker) Check(context.Context, string) model.ProxyExitIPCheckResult {
	return model.ProxyExitIPCheckResult{
		Outcome: model.ProxyExitIPOutcomeInconclusive,
		Reason:  "ip_echo_unavailable",
	}
}

func parseExitIPTargetURL(raw string) (*url.URL, error) {
	targetURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || targetURL == nil || targetURL.Host == "" ||
		(targetURL.Scheme != "http" && targetURL.Scheme != "https") || targetURL.User != nil {
		return nil, errors.New("proxy exit IP check URL is invalid")
	}
	return targetURL, nil
}

func parseExitIPProxyURL(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(model.NormalizeProxyURL(raw))
	if err != nil || proxyURL == nil || proxyURL.Host == "" ||
		(proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return nil, errors.New("proxy URL is invalid")
	}
	return proxyURL, nil
}

func (c *exitIPChecker) Check(ctx context.Context, rawProxyURL string) model.ProxyExitIPCheckResult {
	if c == nil || c.targetURL == nil || c.fetcher == nil {
		return model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeInconclusive, Reason: "ip_echo_unavailable"}
	}
	proxyURL, err := parseExitIPProxyURL(rawProxyURL)
	if err != nil {
		return model.ProxyExitIPCheckResult{Outcome: model.ProxyExitIPOutcomeProxyFailed, Reason: "proxy_url_invalid"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := c.now()
	attempts := c.attempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			return c.result(startedAt, model.ProxyExitIPOutcomeInconclusive, "", "check_cancelled")
		}
		attemptCtx, cancel := context.WithTimeout(ctx, c.attemptTimeout)
		fetched := c.fetcher.Fetch(attemptCtx, proxyURL, c.targetURL)
		cancel()
		if fetched.Err != nil {
			outcome, reason, retryable := classifyExitIPFetchError(fetched.Err, ctx)
			if retryable && attempt+1 < attempts && ctx.Err() == nil {
				continue
			}
			return c.result(startedAt, outcome, "", reason)
		}
		if fetched.StatusCode == http.StatusProxyAuthRequired {
			return c.result(startedAt, model.ProxyExitIPOutcomeProxyFailed, "", "proxy_auth_407")
		}
		if fetched.StatusCode < http.StatusOK || fetched.StatusCode >= http.StatusMultipleChoices {
			return c.result(startedAt, model.ProxyExitIPOutcomeInconclusive, "", echoHTTPReason(fetched.StatusCode))
		}
		if fetched.BodyTooLarge {
			return c.result(startedAt, model.ProxyExitIPOutcomeInconclusive, "", "ip_echo_invalid_response")
		}
		exitIP := parseExitIPResponse(fetched.Body)
		if exitIP == "" {
			return c.result(startedAt, model.ProxyExitIPOutcomeInconclusive, "", "ip_echo_invalid_response")
		}
		return c.result(startedAt, model.ProxyExitIPOutcomeHealthy, exitIP, "")
	}
	return c.result(startedAt, model.ProxyExitIPOutcomeInconclusive, "", "ip_echo_unavailable")
}

func (c *exitIPChecker) result(startedAt time.Time, outcome model.ProxyExitIPOutcome, exitIP, reason string) model.ProxyExitIPCheckResult {
	now := time.Now
	if c != nil && c.now != nil {
		now = c.now
	}
	latency := now().Sub(startedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return model.ProxyExitIPCheckResult{Outcome: outcome, ExitIP: exitIP, LatencyMS: latency, Reason: reason}
}

func parseExitIPResponse(body []byte) string {
	var payload struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	ip := strings.TrimSpace(payload.IP)
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

func echoHTTPReason(statusCode int) string {
	if statusCode == http.StatusTooManyRequests {
		return "ip_echo_http_429"
	}
	if statusCode >= 400 && statusCode < 500 {
		return "ip_echo_http_4xx"
	}
	if statusCode >= 500 && statusCode < 600 {
		return "ip_echo_http_5xx"
	}
	return "ip_echo_invalid_response"
}

func classifyExitIPFetchError(err error, parent context.Context) (model.ProxyExitIPOutcome, string, bool) {
	if err == nil {
		return model.ProxyExitIPOutcomeInconclusive, "ip_echo_unavailable", false
	}
	if parent != nil && errors.Is(parent.Err(), context.Canceled) {
		return model.ProxyExitIPOutcomeInconclusive, "check_cancelled", false
	}
	if errors.Is(err, context.Canceled) {
		return model.ProxyExitIPOutcomeInconclusive, "check_cancelled", false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "407") && strings.Contains(message, "proxy") {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_auth_407", false
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_dns_failed", true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_connect_refused", true
	}
	if isTLSError(err, message) {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_tls_failed", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_connect_timeout", true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return model.ProxyExitIPOutcomeProxyFailed, "proxy_connect_timeout", true
	}
	return model.ProxyExitIPOutcomeProxyFailed, "proxy_transport_failed", true
}

func isTLSError(err error, message string) bool {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return true
	}
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &invalidCertificate) {
		return true
	}
	return strings.Contains(message, "tls:") || strings.Contains(message, "x509:")
}

type httpExitIPFetcher struct{}

func (httpExitIPFetcher) Fetch(ctx context.Context, proxyURL, targetURL *url.URL) exitIPFetchResult {
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxyURL),
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   exitIPCheckAttemptTimeout,
		ResponseHeaderTimeout: exitIPCheckAttemptTimeout,
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return exitIPFetchResult{Err: err}
	}
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return exitIPFetchResult{Err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, exitIPCheckResponseLimit+1))
	if err != nil {
		return exitIPFetchResult{StatusCode: response.StatusCode, Err: err}
	}
	return exitIPFetchResult{
		StatusCode:   response.StatusCode,
		Body:         body[:min(len(body), exitIPCheckResponseLimit)],
		BodyTooLarge: len(body) > exitIPCheckResponseLimit,
	}
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
