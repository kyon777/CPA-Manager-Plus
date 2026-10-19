package model

const (
	ServerErrorPriorityDemotionPending   = "pending"
	ServerErrorPriorityDemotionLeased    = "leased"
	ServerErrorPriorityDemotionRetryWait = "retry_wait"
	ServerErrorPriorityDemotionCompleted = "completed"
	ServerErrorPriorityDemotionFailed    = "failed"
	ServerErrorPriorityDemotionIgnored   = "ignored"
)

// ServerErrorPriorityDemotionJob is a durable, redacted request to apply one
// approved priority demotion. It intentionally contains identity snapshots but
// no management key, credential JSON, token, proxy, or raw failure body.
type ServerErrorPriorityDemotionJob struct {
	ID                int64
	EventHash         string
	SourceEventID     int64
	CredentialKey     string
	FileName          string
	AuthIndex         string
	Provider          string
	AccountSnapshot   string
	AccountIDSnapshot string
	StatusCode        int
	QualificationCode string
	EventAtMS         int64
	State             string
	AvailableAtMS     int64
	LeaseToken        string
	LeaseUntilMS      int64
	AttemptCount      int
	PriorityBefore    *int
	PriorityTarget    *int
	LastErrorCode     string
	LastError         string
	CompletedReason   string
	CreatedAtMS       int64
	UpdatedAtMS       int64
	CompletedAtMS     int64
}

type ServerErrorPriorityDemotionEnqueueResult struct {
	Admitted        int
	Duplicate       int
	Ignored         int
	SkippedDisabled int
	SkippedFloor    int
}

type ServerErrorPriorityDemotionReconcileResult struct {
	Scanned         int
	Admitted        int
	Duplicate       int
	Ignored         int
	SkippedDisabled int
	CursorEventID   int64
}

type ServerErrorPriorityDemotionStats struct {
	Pending       int64
	Leased        int64
	RetryWait     int64
	Completed     int64
	Failed        int64
	Ignored       int64
	OldestReadyMS int64
}
