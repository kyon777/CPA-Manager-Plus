// Package derivedstore contains the explicit source/sink boundary used by
// separate derived storage. The primary database is a read-only Source; the
// disposable database is the only Sink. Keeping these contracts in a package
// that does not import Store prevents accidental cross-database transactions.
package derivedstore

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Stream string

const (
	StreamAccountHistory       Stream = "account-history"
	StreamPricing              Stream = "pricing"
	StreamDashboardHourly      Stream = "dashboard-hourly"
	StreamMonitoringProjection Stream = "monitoring-projection"
	StreamMonitoringMetadata   Stream = "monitoring-metadata"
	StreamMonitoringStats      Stream = "monitoring-stats"
)

type SourceIdentity struct {
	Epoch string
}

type SourceSnapshot struct {
	Identity          SourceIdentity
	TargetEventID     int64
	ArchiveGeneration string
	PricingRevision   string
}

// EventRecord intentionally contains only fields needed by usage derivation.
// Request bodies, raw JSON, fail bodies, access tokens and proxy metadata are
// never copied into the derived database.
type EventRecord struct {
	ID                    int64
	EventHash             string
	TimestampMS           int64
	Provider              string
	ExecutorType          string
	Model                 string
	RequestedModel        string
	ResolvedModel         string
	AuthIndex             string
	Source                string
	SourceHash            string
	APIKeyHash            string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	AuthFileSnapshot      string
	AuthProviderSnapshot  string
	AuthAccountIDSnapshot string
	AuthProjectIDSnapshot string
	ServiceTier           string
	InputTokens           int64
	OutputTokens          int64
	ReasoningTokens       int64
	CachedTokens          int64
	CacheTokens           int64
	CacheReadTokens       int64
	CacheCreationTokens   int64
	NormalizedInputTokens int64
	TotalTokens           int64
	LatencyMS             *int64
	Failed                bool
	FailStatusCode        int
	FailSummary           string
	HeaderQuotaRecoverAt  int64
	HeaderQuotaUsed       *float64
	HeaderQuotaPlanType   string
	HeaderErrorKind       string
	HeaderErrorCode       string
	HeaderTraceID         string
}

type Source interface {
	Identity(context.Context) (SourceIdentity, error)
	Snapshot(context.Context) (SourceSnapshot, error)
	EventsAfter(context.Context, int64, int64, int) ([]EventRecord, error)
	EventsForSubjectsAfter(context.Context, []string, int64, int64, int) ([]EventRecord, error)
	ArchivedEventsAfter(context.Context, int64, int64, int) ([]EventRecord, error)
	PricingRevision(context.Context) (string, error)
	ModelPrices(context.Context) (map[string]model.ModelPrice, error)
}

type StreamState struct {
	Stream            Stream
	SourceEpoch       string
	Generation        string
	Status            string
	CoverageEventID   int64
	TargetEventID     int64
	PricingRevision   string
	ArchiveGeneration string
	ProcessedEvents   int64
	LastSuccessAtMS   int64
	LastErrorCode     string
}

type MaterializedRow struct {
	Stream     Stream
	Generation string
	Key        string
	// SubjectKey is the opaque account identity used to fetch only the visible
	// page's projections. It is never a file name, proxy URL, token, or email.
	SubjectKey   string
	PayloadJSON  string
	FirstEventID int64
	LastEventID  int64
	UpdatedAtMS  int64
}

// AccountAggregate is the redacted, rebuildable account projection shared by
// the history and pricing streams. It deliberately contains no credential,
// proxy, request-body, or raw error payload data.
type AccountAggregate struct {
	AccountKey              string `json:"accountKey"`
	AccountSnapshot         string `json:"accountSnapshot,omitempty"`
	AuthLabelSnapshot       string `json:"authLabelSnapshot,omitempty"`
	AuthProviderSnapshot    string `json:"authProviderSnapshot,omitempty"`
	AuthIndex               string `json:"authIndex,omitempty"`
	Source                  string `json:"source,omitempty"`
	SourceHash              string `json:"sourceHash,omitempty"`
	Model                   string `json:"model"`
	BillingModel            string `json:"billingModel"`
	PricingModel            string `json:"pricingModel,omitempty"`
	ServiceTier             string `json:"serviceTier,omitempty"`
	ContextThresholdTokens  int64  `json:"contextThresholdTokens,omitempty"`
	Calls                   int64  `json:"calls"`
	SuccessCalls            int64  `json:"successCalls"`
	FailureCalls            int64  `json:"failureCalls"`
	InputTokens             int64  `json:"inputTokens"`
	OutputTokens            int64  `json:"outputTokens"`
	ReasoningTokens         int64  `json:"reasoningTokens"`
	CachedTokens            int64  `json:"cachedTokens"`
	CacheReadTokens         int64  `json:"cacheReadTokens"`
	CacheCreationTokens     int64  `json:"cacheCreationTokens"`
	LongInputTokens         int64  `json:"longInputTokens"`
	LongOutputTokens        int64  `json:"longOutputTokens"`
	LongCachedTokens        int64  `json:"longCachedTokens"`
	LongCacheReadTokens     int64  `json:"longCacheReadTokens"`
	LongCacheCreationTokens int64  `json:"longCacheCreationTokens"`
	TotalTokens             int64  `json:"totalTokens"`
	FirstSeenMS             int64  `json:"firstSeenMs"`
	LastSeenMS              int64  `json:"lastSeenMs"`
	UpdatedAtMS             int64  `json:"updatedAtMs"`
}

type ApplyBatch struct {
	Stream            Stream
	SourceEpoch       string
	Generation        string
	PricingRevision   string
	ArchiveGeneration string
	FromEventID       int64
	ToEventID         int64
	Events            []EventRecord
	Rows              []MaterializedRow
	AppliedAtMS       int64
}

type Status struct {
	SourceEpoch string
	Streams     []StreamState
}

type Sink interface {
	State(context.Context, Stream) (StreamState, error)
	Rows(context.Context, Stream, string) ([]MaterializedRow, error)
	RowsByKeys(context.Context, Stream, string, []string) ([]MaterializedRow, error)
	RowsForSubjects(context.Context, Stream, string, []string) ([]MaterializedRow, error)
	Apply(context.Context, ApplyBatch) (StreamState, error)
	Status(context.Context) (Status, error)
}
