package credentiallist

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestComputeGlobalMetricsPartitionsTheCompleteInventory(t *testing.T) {
	items := []map[string]any{
		{"name": "new.json", "disabled": false},
		{"name": "disabled.json", "disabled": true},
		{"name": "reauth.json", "disabled": false},
		{"name": "quota.json", "disabled": false},
		{"name": "healthy.json", "disabled": false},
	}
	evidence := map[string]GlobalCredentialEvidence{
		itemKey(items[2]): {HasEvidence: true, NeedsAttention: true},
		itemKey(items[3]): {HasEvidence: true, QuotaRisk: true},
		itemKey(items[4]): {HasEvidence: true, Available: true},
	}

	got := ComputeGlobalMetrics(items, evidence)
	want := GlobalMetrics{
		Total:                 5,
		Enabled:               4,
		Available:             1,
		NeedsAttention:        1,
		QuotaRisk:             1,
		Disabled:              1,
		Unconfirmed:           1,
		NeedsInspectionAction: 0,
	}
	if got != want {
		t.Fatalf("global metrics = %#v, want %#v", got, want)
	}
}

func TestGlobalMetricsReaderIsCalledOnlyForACompleteSnapshot(t *testing.T) {
	reader := &fakeInventoryReader{pages: map[int]string{
		1: `{"files":[{"name":"a.json"}],"total":2,"has_more":true}`,
		2: `{"files":[{"name":"b.json"}],"total":2,"has_more":false}`,
	}}
	metricsReader := &fakeGlobalMetricsReader{
		metrics: GlobalMetrics{Total: 2, Enabled: 2, Unconfirmed: 2},
	}
	service := NewWithGlobalMetricsReader(reader, metricsReader)

	first, err := service.List(context.Background(), Request{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("first list: %v", err)
	}
	if first.GlobalMetricsStatus == globalMetricsReady {
		t.Fatal("warming inventory must not report a ready global summary")
	}

	// The background warm fetch is deterministic for this fake reader. Polling
	// the same endpoint gives the summary once the complete snapshot is ready.
	for attempt := 0; attempt < 20 && metricsReader.calls == 0; attempt++ {
		_, _ = service.List(context.Background(), Request{Page: 1, PageSize: 1})
		time.Sleep(5 * time.Millisecond)
	}
	if metricsReader.calls != 1 {
		t.Fatalf("global metrics reader calls = %d, want one after complete snapshot", metricsReader.calls)
	}
}

func TestWeeklyQuotaCanBeCoveredByPositiveCredits(t *testing.T) {
	header := store.HeaderSnapshot{
		HeaderQuotaRecoverAtMS: sql.NullInt64{Int64: 2_000, Valid: true},
		ResponseMetadata: &usage.ResponseHeaderMetadata{
			Quota: &usage.HeaderQuotaMetadata{
				CreditsBalance:    "12.5",
				ReachedWindowKind: "weekly",
			},
		},
	}
	quotaRisk, available, _ := classifyHeaderEvidence(header, 1_000)
	if quotaRisk || !available {
		t.Fatalf("weekly quota with credits = quotaRisk %v, available %v; want no risk and available", quotaRisk, available)
	}
}

func TestFiveHourQuotaIsNotCoveredByPositiveCredits(t *testing.T) {
	header := store.HeaderSnapshot{
		HeaderQuotaRecoverAtMS: sql.NullInt64{Int64: 2_000, Valid: true},
		ResponseMetadata: &usage.ResponseHeaderMetadata{
			Quota: &usage.HeaderQuotaMetadata{
				CreditsBalance:    "12.5",
				ReachedWindowKind: "5h",
			},
		},
	}
	quotaRisk, available, _ := classifyHeaderEvidence(header, 1_000)
	if !quotaRisk || available {
		t.Fatalf("five-hour quota with credits = quotaRisk %v, available %v; want risk only", quotaRisk, available)
	}
}

func TestPositiveCreditsWithoutWindowEvidenceRemainAvailable(t *testing.T) {
	header := store.HeaderSnapshot{
		HeaderQuotaRecoverAtMS: sql.NullInt64{Int64: 2_000, Valid: true},
		ResponseMetadata: &usage.ResponseHeaderMetadata{
			Quota: &usage.HeaderQuotaMetadata{CreditsBalance: "12.5"},
		},
	}
	quotaRisk, available, _ := classifyHeaderEvidence(header, 1_000)
	if quotaRisk || !available {
		t.Fatalf("positive credits without a window = quotaRisk %v, available %v; want available", quotaRisk, available)
	}
}

type fakeGlobalMetricsReader struct {
	metrics GlobalMetrics
	calls   int
}

func (r *fakeGlobalMetricsReader) ReadGlobalMetrics(_ context.Context, items []map[string]any) (GlobalMetrics, error) {
	r.calls++
	return r.metrics, nil
}
