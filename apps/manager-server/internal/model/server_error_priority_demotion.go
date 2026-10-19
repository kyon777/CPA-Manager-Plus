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

// ServerErrorPriorityDemotionCursor is a stable position in the completed
// priority-demotion stream. Both fields are required because several jobs can
// complete in the same millisecond.
type ServerErrorPriorityDemotionCursor struct {
	CompletedAtMS int64 `json:"completedAtMs"`
	ID            int64 `json:"id"`
}

// ServerErrorPriorityDemotionUpdate is the redacted, UI-safe subset of an
// applied priority-demotion job. It deliberately excludes the raw request,
// account snapshots, proxy URL, and every credential secret.
type ServerErrorPriorityDemotionUpdate struct {
	ID             int64  `json:"id"`
	FileName       string `json:"fileName"`
	AuthIndex      string `json:"authIndex"`
	Provider       string `json:"provider"`
	PriorityBefore int    `json:"priorityBefore"`
	PriorityTarget int    `json:"priorityTarget"`
	CompletedAtMS  int64  `json:"completedAtMs"`
}

// ServerErrorPriorityDemotionUpdatePage contains all applied changes after a
// cursor and the exact cursor the caller should persist for its next poll.
type ServerErrorPriorityDemotionUpdatePage struct {
	Items      []ServerErrorPriorityDemotionUpdate `json:"items"`
	NextCursor ServerErrorPriorityDemotionCursor   `json:"nextCursor"`
}
