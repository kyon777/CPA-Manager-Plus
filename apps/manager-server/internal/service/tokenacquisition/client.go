// Package tokenacquisition implements the server-only TokenAcquisition v1
// contract. Its result is intentionally an internal value; callers must never
// serialize it to a browser, log it, or persist it in task state.
package tokenacquisition

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
	"unicode"
	"unicode/utf8"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultTimeout      = 14*time.Minute + 30*time.Second
	maxResponseBytes    = 1 << 20
	maxFailureCodeBytes = 96
	maxFailureTextBytes = 320
)

var (
	ErrNotConfigured = errors.New("token acquisition is not configured")
	ErrConflict      = errors.New("token acquisition already has an active job")
	ErrRequestFailed = errors.New("token acquisition request failed")
	ErrInvalidResult = errors.New("token acquisition returned an invalid result")
	ErrJobFailed     = errors.New("token acquisition job failed")
	ErrTimedOut      = errors.New("token acquisition timed out")
)

// Request carries a single account. HTTPProxy is only populated from the
// current CPA credential and is sent to TokenAcquisition; it is never written
// back from an external result.
type Request struct {
	Email     string
	HTTPProxy string
}

// Result is held in memory only until the caller merges it into CPA Core.
type Result struct {
	Email            string
	AccessToken      string
	RefreshToken     string
	IDToken          string
	ChatGPTAccountID string
}

// ExternalFailure retains only the structured, user-actionable code/message
// returned by TokenAcquisition. Its Error method deliberately returns the
// underlying category only, so callers that log the error do not accidentally
// log external response text, credentials, or proxy information.
type ExternalFailure struct {
	cause   error
	code    string
	message string
}

func (e *ExternalFailure) Error() string {
	if e == nil || e.cause == nil {
		return "token acquisition failed"
	}
	return e.cause.Error()
}

func (e *ExternalFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// FailureReason returns a bounded, one-line reason that originated from a
// structured TokenAcquisition response. It never returns an arbitrary raw
// response body or a locally generated error string.
func FailureReason(err error) string {
	var failure *ExternalFailure
	if !errors.As(err, &failure) || failure == nil {
		return ""
	}
	code := sanitizeFailureCode(failure.code)
	message := sanitizeFailureText(failure.message)
	switch {
	case code != "" && message != "":
		return code + ": " + message
	case message != "":
		return message
	default:
		return code
	}
}

type Acquirer interface {
	Acquire(context.Context, Request) (Result, error)
}

type Config struct {
	BaseURL      string
	APIKey       string
	HTTPClient   *http.Client
	PollInterval time.Duration
	Timeout      time.Duration
}

type Client struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	pollInterval time.Duration
	timeout      time.Duration
}

func New(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL:      strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		apiKey:       strings.TrimSpace(cfg.APIKey),
		httpClient:   httpClient,
		pollInterval: pollInterval,
		timeout:      timeout,
	}
}

func (c *Client) Acquire(ctx context.Context, request Request) (Result, error) {
	if c == nil || c.baseURL == "" || c.apiKey == "" {
		return Result{}, ErrNotConfigured
	}
	if strings.TrimSpace(request.Email) == "" {
		return Result{}, ErrInvalidResult
	}
	parsedBase, err := url.ParseRequestURI(c.baseURL)
	if err != nil || parsedBase.Host == "" ||
		(!strings.EqualFold(parsedBase.Scheme, "http") && !strings.EqualFold(parsedBase.Scheme, "https")) ||
		parsedBase.User != nil {
		return Result{}, ErrNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	batchID, err := c.createBatch(ctx, request)
	if err != nil {
		return Result{}, normalizeContextError(err)
	}
	for {
		batch, err := c.getBatch(ctx, batchID)
		if err != nil {
			return Result{}, normalizeContextError(err)
		}
		if batch.Done {
			return resultFromBatch(batch, request.Email)
		}
		if err := wait(ctx, c.pollInterval); err != nil {
			return Result{}, normalizeContextError(err)
		}
	}
}

func (c *Client) createBatch(ctx context.Context, request Request) (string, error) {
	type account struct {
		Email string `json:"email"`
		Proxy string `json:"proxy,omitempty"`
	}
	payload := struct {
		Accounts []account `json:"accounts"`
		Direct   bool      `json:"direct"`
	}{
		Accounts: []account{{Email: strings.TrimSpace(request.Email), Proxy: strings.TrimSpace(request.HTTPProxy)}},
		Direct:   strings.TrimSpace(request.HTTPProxy) == "",
	}
	var response struct {
		BatchID string `json:"batch_id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/tokens", payload, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.BatchID) == "" {
		return "", ErrInvalidResult
	}
	return strings.TrimSpace(response.BatchID), nil
}

type batchResponse struct {
	Done bool `json:"done"`
	Jobs []struct {
		Email  string `json:"email"`
		Status string `json:"status"`
		Result *struct {
			Email            string `json:"email"`
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			IDToken          string `json:"id_token"`
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"result"`
		Error *apiErrorDetail `json:"error"`
	} `json:"jobs"`
}

type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) getBatch(ctx context.Context, batchID string) (batchResponse, error) {
	var response batchResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/batches/"+url.PathEscape(batchID), nil, &response); err != nil {
		return batchResponse{}, err
	}
	return response, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, output any) error {
	endpoint := c.baseURL + path
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return ErrRequestFailed
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ErrRequestFailed
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	// Never follow a redirect while carrying the administrator API key. A
	// redirecting upstream must fail closed rather than forwarding credentials
	// to a different origin.
	client := *c.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		failureCause := fmt.Errorf("%w: status %d", ErrRequestFailed, resp.StatusCode)
		if resp.StatusCode == http.StatusConflict {
			failureCause = ErrConflict
		}
		responseBody, readErr := readResponseBody(resp.Body)
		if readErr != nil {
			return failureCause
		}
		return newExternalFailure(failureCause, parseAPIErrorDetail(responseBody))
	}
	responseBody, err := readResponseBody(resp.Body)
	if err != nil {
		return ErrInvalidResult
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(output); err != nil {
		return ErrInvalidResult
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidResult
	}
	return nil
}

func resultFromBatch(batch batchResponse, expectedEmail string) (Result, error) {
	expectedEmail = normalizeEmail(expectedEmail)
	if expectedEmail == "" {
		return Result{}, ErrInvalidResult
	}
	for _, job := range batch.Jobs {
		if normalizeEmail(job.Email) != expectedEmail {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(job.Status), "error") {
			return Result{}, newExternalFailure(ErrJobFailed, apiErrorDetailFromPointer(job.Error))
		}
		if !strings.EqualFold(strings.TrimSpace(job.Status), "ok") || job.Result == nil {
			return Result{}, ErrInvalidResult
		}
		result := Result{
			Email:            strings.TrimSpace(job.Result.Email),
			AccessToken:      strings.TrimSpace(job.Result.AccessToken),
			RefreshToken:     strings.TrimSpace(job.Result.RefreshToken),
			IDToken:          strings.TrimSpace(job.Result.IDToken),
			ChatGPTAccountID: strings.TrimSpace(job.Result.ChatGPTAccountID),
		}
		if normalizeEmail(result.Email) != expectedEmail ||
			strings.TrimSpace(result.AccessToken) == "" ||
			strings.TrimSpace(result.RefreshToken) == "" ||
			strings.TrimSpace(result.IDToken) == "" {
			return Result{}, ErrInvalidResult
		}
		return result, nil
	}
	return Result{}, ErrInvalidResult
}

func readResponseBody(body io.Reader) ([]byte, error) {
	responseBody, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil || len(responseBody) > maxResponseBytes {
		return nil, ErrInvalidResult
	}
	return responseBody, nil
}

func parseAPIErrorDetail(body []byte) apiErrorDetail {
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Detail) == 0 {
		return apiErrorDetail{}
	}
	var detail apiErrorDetail
	if err := json.Unmarshal(envelope.Detail, &detail); err == nil {
		return detail
	}
	var message string
	if err := json.Unmarshal(envelope.Detail, &message); err == nil {
		return apiErrorDetail{Message: message}
	}
	return apiErrorDetail{}
}

func apiErrorDetailFromPointer(detail *apiErrorDetail) apiErrorDetail {
	if detail == nil {
		return apiErrorDetail{}
	}
	return *detail
}

func newExternalFailure(cause error, detail apiErrorDetail) error {
	code := sanitizeFailureCode(detail.Code)
	message := sanitizeFailureText(detail.Message)
	if code == "" && message == "" {
		return cause
	}
	return &ExternalFailure{cause: cause, code: code, message: message}
}

func sanitizeFailureCode(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLower(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			if builder.Len()+utf8.RuneLen(r) > maxFailureCodeBytes {
				break
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func sanitizeFailureText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	pendingSpace := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			pendingSpace = builder.Len() > 0
			continue
		}
		if pendingSpace {
			if builder.Len()+1 > maxFailureTextBytes {
				break
			}
			builder.WriteByte(' ')
			pendingSpace = false
		}
		if builder.Len()+utf8.RuneLen(r) > maxFailureTextBytes {
			break
		}
		builder.WriteRune(r)
	}
	message := strings.TrimSpace(builder.String())
	if hasSensitiveFailureText(message) {
		// Keep the structured error code but suppress an unsafe message. This
		// value is stored in SQLite and returned to the browser, so it must not
		// become a second channel for tokens, cookies, API keys, or proxy
		// credentials returned by an upstream implementation.
		return ""
	}
	return message
}

func hasSensitiveFailureText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"://",
		"access_token",
		"access-token",
		"access token",
		"refresh_token",
		"refresh-token",
		"refresh token",
		"id_token",
		"id-token",
		"id token",
		"api_key",
		"api-key",
		"api key",
		"authorization",
		"bearer ",
		"cookie",
		"password",
		"secret",
		"token=",
		"token:",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return err
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
