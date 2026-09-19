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
)

const (
	defaultPollInterval = 2 * time.Second
	defaultTimeout      = 14*time.Minute + 30*time.Second
	maxResponseBytes    = 1 << 20
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
	if _, err := url.ParseRequestURI(c.baseURL); err != nil {
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
	} `json:"jobs"`
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
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: status %d", ErrRequestFailed, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(output); err != nil {
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
			return Result{}, ErrJobFailed
		}
		if !strings.EqualFold(strings.TrimSpace(job.Status), "ok") || job.Result == nil {
			return Result{}, ErrInvalidResult
		}
		result := Result{
			Email:            strings.TrimSpace(job.Result.Email),
			AccessToken:      job.Result.AccessToken,
			RefreshToken:     job.Result.RefreshToken,
			IDToken:          job.Result.IDToken,
			ChatGPTAccountID: job.Result.ChatGPTAccountID,
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
