package model

const (
	TokenRecoveryModeAuto   = "auto"
	TokenRecoveryModeManual = "manual"

	TokenRecoveryStatusAutoQueued             = "auto_queued"
	TokenRecoveryStatusAutoRunning            = "auto_running"
	TokenRecoveryStatusSucceeded              = "succeeded"
	TokenRecoveryStatusAutoFailedManualOnly   = "auto_failed_manual_only"
	TokenRecoveryStatusManualQueued           = "manual_queued"
	TokenRecoveryStatusManualRunning          = "manual_running"
	TokenRecoveryStatusManualFailedManualOnly = "manual_failed_manual_only"
)

// TokenRecoveryTarget deliberately contains no account ID, token, proxy or
// credential JSON. The server resolves current credential data from CPA Core.
type TokenRecoveryTarget struct {
	FileName     string `json:"fileName"`
	AuthIndex    string `json:"authIndex,omitempty"`
	AccountEmail string `json:"accountEmail,omitempty"`
	Provider     string `json:"provider"`
	ObservedAtMS int64  `json:"observedAtMs,omitempty"`
	// ObservedStatusCode is evidence supplied only by the event/inspection
	// producer. Automatic recovery is deliberately limited to a confirmed 401.
	ObservedStatusCode int `json:"observedStatusCode,omitempty"`
}

// TokenRecoveryTask is the redacted durable task state allowed to leave the
// Manager Server. ErrorCode is intentionally a short, non-sensitive category;
// LastErrorMessage is populated only from a bounded, structured external
// TokenAcquisition failure reason.
type TokenRecoveryTask struct {
	ID               int64  `json:"id"`
	FileName         string `json:"fileName"`
	AuthIndex        string `json:"authIndex,omitempty"`
	AccountEmail     string `json:"accountEmail,omitempty"`
	Provider         string `json:"provider"`
	Status           string `json:"status"`
	Mode             string `json:"mode"`
	LastErrorCode    string `json:"lastErrorCode,omitempty"`
	LastErrorMessage string `json:"lastErrorMessage,omitempty"`
	// AutoAttemptedAtMS is an immutable audit marker for the first automatic
	// recovery cycle. Manual retries deliberately retain it so a later 401
	// cannot silently create another automatic external acquisition.
	AutoAttemptedAtMS int64 `json:"autoAttemptedAtMs,omitempty"`
	LastSignalAtMS    int64 `json:"lastSignalAtMs"`
	StartedAtMS       int64 `json:"startedAtMs,omitempty"`
	CompletedAtMS     int64 `json:"completedAtMs,omitempty"`
	CreatedAtMS       int64 `json:"createdAtMs"`
	UpdatedAtMS       int64 `json:"updatedAtMs"`
}
