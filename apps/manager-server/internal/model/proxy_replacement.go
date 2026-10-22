package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type ProxyPoolStatus string

const (
	ProxyPoolStatusNormal              ProxyPoolStatus = "normal"
	ProxyPoolStatusPendingVerification ProxyPoolStatus = "pending_verification"
	ProxyPoolStatusSuspect             ProxyPoolStatus = "suspect"
)

type ProxyReplacementSource string

const (
	ProxyReplacementSourceInspection   ProxyReplacementSource = "inspection"
	ProxyReplacementSourceBatchUniform ProxyReplacementSource = "batch_uniform"
	ProxyReplacementSourceBatchPool    ProxyReplacementSource = "batch_pool"
)

type ProxyReplacementMode string

const (
	ProxyReplacementModeUniform ProxyReplacementMode = "uniform"
	ProxyReplacementModePool    ProxyReplacementMode = "pool"
)

type ProxyReplacementTaskStatus string

const (
	ProxyReplacementTaskDraft       ProxyReplacementTaskStatus = "draft"
	ProxyReplacementTaskQueued      ProxyReplacementTaskStatus = "queued"
	ProxyReplacementTaskRunning     ProxyReplacementTaskStatus = "running"
	ProxyReplacementTaskCompleted   ProxyReplacementTaskStatus = "completed"
	ProxyReplacementTaskPartial     ProxyReplacementTaskStatus = "partial"
	ProxyReplacementTaskFailed      ProxyReplacementTaskStatus = "failed"
	ProxyReplacementTaskExpired     ProxyReplacementTaskStatus = "expired"
	ProxyReplacementTaskUnavailable ProxyReplacementTaskStatus = "unavailable"
)

type ProxyReplacementItemStatus string

const (
	ProxyReplacementItemPlanned          ProxyReplacementItemStatus = "planned"
	ProxyReplacementItemWriting          ProxyReplacementItemStatus = "writing"
	ProxyReplacementItemUpdated          ProxyReplacementItemStatus = "updated"
	ProxyReplacementItemSkippedStale     ProxyReplacementItemStatus = "skipped_stale"
	ProxyReplacementItemWriteFailed      ProxyReplacementItemStatus = "write_failed"
	ProxyReplacementItemRecheckRunning   ProxyReplacementItemStatus = "recheck_running"
	ProxyReplacementItemRecheckSucceeded ProxyReplacementItemStatus = "recheck_succeeded"
	ProxyReplacementItemRecheckFailed    ProxyReplacementItemStatus = "recheck_failed"
	// ProxyReplacementItemRecheckInconclusive means the fixed exit-IP probe
	// could not establish either health or a proxy failure. It is deliberately
	// distinct from a direct proxy failure.
	ProxyReplacementItemRecheckInconclusive ProxyReplacementItemStatus = "recheck_inconclusive"
)

type ProxyReplacementTarget struct {
	ClientKey            string `json:"clientKey"`
	FileName             string `json:"fileName"`
	AuthIndex            string `json:"authIndex,omitempty"`
	AccountEmail         string `json:"accountEmail,omitempty"`
	Provider             string `json:"provider"`
	RuntimeID            string `json:"runtimeId,omitempty"`
	AccountID            string `json:"accountId,omitempty"`
	AccountSnapshot      string `json:"accountSnapshot,omitempty"`
	InspectionRunID      int64  `json:"inspectionRunId,omitempty"`
	InspectionAccountKey string `json:"inspectionAccountKey,omitempty"`
}

// ProxyReplacementPreviewRequest is the browser-to-Manager request. It has no
// management credentials, raw auth JSON, quota fields, or arbitrary patch map.
type ProxyReplacementPreviewRequest struct {
	Source          ProxyReplacementSource   `json:"source"`
	Mode            ProxyReplacementMode     `json:"mode"`
	UniformProxyURL string                   `json:"uniformProxyUrl,omitempty"`
	Targets         []ProxyReplacementTarget `json:"targets"`
}

type ProxyPoolEntry struct {
	URL   string         `json:"url"`
	State ProxyPoolState `json:"state"`
}

type ProxyPoolState struct {
	Fingerprint       string          `json:"fingerprint"`
	Status            ProxyPoolStatus `json:"status"`
	LastFailureAtMS   int64           `json:"lastFailureAtMs,omitempty"`
	LastFailureReason string          `json:"lastFailureReason,omitempty"`
	UpdatedAtMS       int64           `json:"updatedAtMs"`
}

// ProxyExitIPOutcome is the result of a direct CPAM-to-proxy exit-IP check.
// It never includes a proxy URL because those URLs can contain credentials.
type ProxyExitIPOutcome string

const (
	ProxyExitIPOutcomeHealthy      ProxyExitIPOutcome = "healthy"
	ProxyExitIPOutcomeProxyFailed  ProxyExitIPOutcome = "proxy_failed"
	ProxyExitIPOutcomeInconclusive ProxyExitIPOutcome = "inconclusive"
	ProxyExitIPOutcomeNotSaved     ProxyExitIPOutcome = "not_saved"
)

type ProxyExitIPCheckResult struct {
	Fingerprint string             `json:"fingerprint"`
	Outcome     ProxyExitIPOutcome `json:"outcome"`
	ExitIP      string             `json:"exitIp,omitempty"`
	LatencyMS   int64              `json:"latencyMs,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

type ProxyExitIPCheckBatch struct {
	Results []ProxyExitIPCheckResult `json:"results"`
}

// CredentialProxyExitIPCheckResult binds direct exit-IP evidence to the
// browser-supplied opaque client key. It intentionally contains no proxy URL:
// URLs are resolved only from the current Manager Server credential record.
type CredentialProxyExitIPCheckResult struct {
	ClientKey string             `json:"clientKey"`
	Outcome   ProxyExitIPOutcome `json:"outcome"`
	ExitIP    string             `json:"exitIp,omitempty"`
	LatencyMS int64              `json:"latencyMs,omitempty"`
	Reason    string             `json:"reason,omitempty"`
}

type CredentialProxyExitIPCheckBatch struct {
	Results []CredentialProxyExitIPCheckResult `json:"results"`
}

type ProxyReplacementTask struct {
	ID                       int64                      `json:"id"`
	Source                   ProxyReplacementSource     `json:"source"`
	Mode                     ProxyReplacementMode       `json:"mode"`
	Status                   ProxyReplacementTaskStatus `json:"status"`
	RecheckAfterWrite        bool                       `json:"recheckAfterWrite"`
	CanConfirm               bool                       `json:"canConfirm"`
	TaskOffset               int                        `json:"taskOffset"`
	TotalCount               int                        `json:"totalCount"`
	UpdatedCount             int                        `json:"updatedCount"`
	FailedCount              int                        `json:"failedCount"`
	SkippedCount             int                        `json:"skippedCount"`
	RecheckSuccessCount      int                        `json:"recheckSuccessCount"`
	RecheckFailedCount       int                        `json:"recheckFailedCount"`
	RecheckInconclusiveCount int                        `json:"recheckInconclusiveCount"`
	UnusedNormalCount        int                        `json:"unusedNormalCount"`
	FallbackNormalCount      int                        `json:"fallbackNormalCount"`
	SuspectExcludedCount     int                        `json:"suspectExcludedCount"`
	ExpiresAtMS              int64                      `json:"expiresAtMs,omitempty"`
	CreatedAtMS              int64                      `json:"createdAtMs"`
	UpdatedAtMS              int64                      `json:"updatedAtMs"`
}

type ProxyReplacementTaskItem struct {
	ID                  int64                      `json:"id"`
	TaskID              int64                      `json:"taskId"`
	ItemOrder           int                        `json:"itemOrder"`
	Target              ProxyReplacementTarget     `json:"target"`
	OldProxyURL         string                     `json:"oldProxyUrl,omitempty"`
	OldProxyFingerprint string                     `json:"oldProxyFingerprint,omitempty"`
	NewProxyURL         string                     `json:"newProxyUrl,omitempty"`
	NewProxyFingerprint string                     `json:"newProxyFingerprint,omitempty"`
	AllocationSource    string                     `json:"allocationSource,omitempty"`
	Status              ProxyReplacementItemStatus `json:"status"`
	WriteErrorCode      string                     `json:"writeErrorCode,omitempty"`
	WriteErrorMessage   string                     `json:"writeErrorMessage,omitempty"`
	RecheckErrorCode    string                     `json:"recheckErrorCode,omitempty"`
	RecheckErrorMessage string                     `json:"recheckErrorMessage,omitempty"`
}

type ProxyReplacementTaskPage struct {
	Task     ProxyReplacementTask       `json:"task"`
	Items    []ProxyReplacementTaskItem `json:"items"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"pageSize"`
	Total    int                        `json:"total"`
}

type ProxyReplacementItemOutcome struct {
	WriteErrorCode      string
	WriteErrorMessage   string
	RecheckErrorCode    string
	RecheckErrorMessage string
}

type ProxyReplacementProbeResult struct {
	Target       ProxyReplacementTarget
	Succeeded    bool
	ErrorCode    string
	ErrorMessage string
	ProxySuspect bool
}

type ProxyFailureEvidence struct {
	ProxyURL    string
	StatusCode  int
	ErrorKind   string
	ErrorDetail string
}

// NormalizeProxyURL produces the canonical proxy identity used by the pool.
// User-entered whitespace and trailing path separators are not meaningful for
// the saved proxy inventory.
func NormalizeProxyURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

// ProxyFingerprint returns the non-reversible identity persisted for a proxy.
// The raw URL can contain proxy credentials and must not be stored in state or
// task summaries as plaintext.
func ProxyFingerprint(proxyURL string) string {
	canonical := NormalizeProxyURL(proxyURL)
	if canonical == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// IsSuspectedProxyTransportFailure is deliberately narrow: a credential must
// have a configured proxy and the failure must prove a proxy-chain transport
// problem. Authentication, quota, credit, rate-limit, and server business
// responses are not proxy evidence.
func IsSuspectedProxyTransportFailure(evidence ProxyFailureEvidence) bool {
	if NormalizeProxyURL(evidence.ProxyURL) == "" {
		return false
	}

	detail := strings.ToLower(strings.TrimSpace(evidence.ErrorDetail))
	kind := strings.ToLower(strings.TrimSpace(evidence.ErrorKind))
	if evidence.StatusCode == 401 || evidence.StatusCode == 429 ||
		strings.Contains(detail, "quota") || strings.Contains(detail, "credit") {
		return false
	}
	if kind != "request_error" {
		return false
	}

	if strings.Contains(detail, "context deadline exceeded") || strings.Contains(detail, "timeout") {
		return true
	}

	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(detail)
	return strings.Contains(compact, "api-callfailed:502") &&
		strings.Contains(compact, `"error":"requestfailed"`)
}
