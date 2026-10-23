package usageevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRecentProxyOverloadEvidenceUsesLatestNineEventsInsideWindow(t *testing.T) {
	ctx := context.Background()
	db := openProxyOverloadTestDB(t, true)
	repo := New(db)
	now := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)
	nowMS := now.UnixMilli()

	events := make([]usage.Event, 0, 13)
	oldestOfTen := latestAccountRequestEvent(
		"proxy-overload-tenth-oldest",
		nowMS-int64(60*time.Minute/time.Millisecond),
		"ten.json",
		"idx-ten",
		"ten.json",
	)
	oldestOfTen.Failed = true
	oldestOfTen.FailStatusCode = 502
	oldestOfTen.HeaderErrorCode = "server_is_overloaded"
	events = append(events, oldestOfTen)
	for i := 1; i <= 9; i++ {
		events = append(events, latestAccountRequestEvent(
			fmt.Sprintf("proxy-overload-newer-%d", i),
			nowMS-int64((60-i)*int(time.Minute/time.Millisecond)),
			"ten.json",
			"idx-ten",
			"ten.json",
		))
	}
	recentExact := latestAccountRequestEvent(
		"proxy-overload-recent-exact",
		nowMS-int64(time.Minute/time.Millisecond),
		"exact.json",
		"idx-exact",
		"exact.json",
	)
	recentExact.Failed = true
	recentExact.FailStatusCode = 502
	recentExact.HeaderErrorCode = "server_is_overloaded"
	events = append(events, recentExact)

	outsideWindow := latestAccountRequestEvent(
		"proxy-overload-outside-window",
		now.Add(-73*time.Hour).UnixMilli(),
		"old.json",
		"idx-old",
		"old.json",
	)
	outsideWindow.Failed = true
	outsideWindow.FailStatusCode = 502
	outsideWindow.HeaderErrorCode = "server_is_overloaded"
	events = append(events, outsideWindow)

	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	evidence, err := repo.RecentProxyOverloadEvidence(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "ten.json", AuthIndex: "idx-ten"},
		{RequestIndex: 1, AuthFileSnapshot: "exact.json", AuthIndex: "idx-exact"},
		{RequestIndex: 2, AuthFileSnapshot: "old.json", AuthIndex: "idx-old"},
	}, now.Add(-72*time.Hour).UnixMilli(), 9)
	if err != nil {
		t.Fatalf("recent proxy overload evidence: %v", err)
	}
	got := evidenceByRequestIndex(evidence)
	if value, ok := got[0]; !ok || !value.Active || value.ServerOverloaded {
		t.Fatalf("target 0 evidence = %#v, want active non-overloaded newest-nine result", value)
	}
	if value, ok := got[1]; !ok || !value.Active || !value.ServerOverloaded {
		t.Fatalf("target 1 evidence = %#v, want active exact overload result", value)
	}
	if _, ok := got[2]; ok {
		t.Fatalf("outside-window target unexpectedly produced evidence: %#v", got[2])
	}
}

func TestRecentProxyOverloadEvidenceRequiresExactServerOverloadedSignal(t *testing.T) {
	ctx := context.Background()
	db := openProxyOverloadTestDB(t, true)
	repo := New(db)
	nowMS := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC).UnixMilli()

	newEvent := func(hash, name, index string, sequence int) usage.Event {
		return latestAccountRequestEvent(
			hash,
			nowMS-int64(sequence)*int64(time.Minute/time.Millisecond),
			name,
			index,
			name,
		)
	}
	headerExact := newEvent("proxy-overload-header-exact", "header.json", "idx-header", 1)
	headerExact.Failed = true
	headerExact.FailStatusCode = 502
	headerExact.HeaderErrorCode = "server_is_overloaded"

	summaryExact := newEvent("proxy-overload-summary-exact", "summary.json", "idx-summary", 2)
	summaryExact.Failed = true
	summaryExact.FailStatusCode = 502
	summaryExact.FailSummary = "upstream code=\"server_is_overloaded\""

	nearCode := newEvent("proxy-overload-near-code", "near.json", "idx-near", 3)
	nearCode.Failed = true
	nearCode.FailStatusCode = 502
	nearCode.FailSummary = "not_server_is_overloaded"

	plain502 := newEvent("proxy-overload-plain-502", "near.json", "idx-near", 4)
	plain502.Failed = true
	plain502.FailStatusCode = 502
	plain502.FailSummary = "unexpected EOF"

	http500 := newEvent("proxy-overload-500", "near.json", "idx-near", 5)
	http500.Failed = true
	http500.FailStatusCode = 500
	http500.HeaderErrorCode = "server_is_overloaded"

	http429 := newEvent("proxy-overload-429", "near.json", "idx-near", 6)
	http429.Failed = true
	http429.FailStatusCode = 429
	http429.HeaderErrorCode = "server_is_overloaded"

	notFailed := newEvent("proxy-overload-not-failed", "near.json", "idx-near", 7)
	notFailed.Failed = false
	notFailed.FailStatusCode = 502
	notFailed.HeaderErrorCode = "server_is_overloaded"

	if _, err := repo.InsertBatch(ctx, []usage.Event{
		headerExact, summaryExact, nearCode, plain502, http500, http429, notFailed,
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	evidence, err := repo.RecentProxyOverloadEvidence(ctx, []LatestAccountRequestQuery{
		{RequestIndex: 0, AuthFileSnapshot: "header.json", AuthIndex: "idx-header"},
		{RequestIndex: 1, AuthFileSnapshot: "summary.json", AuthIndex: "idx-summary"},
		{RequestIndex: 2, AuthFileSnapshot: "near.json", AuthIndex: "idx-near"},
	}, nowMS-int64(72*time.Hour/time.Millisecond), 9)
	if err != nil {
		t.Fatalf("recent proxy overload evidence: %v", err)
	}
	got := evidenceByRequestIndex(evidence)
	for _, index := range []int{0, 1} {
		if value, ok := got[index]; !ok || !value.Active || !value.ServerOverloaded {
			t.Fatalf("target %d evidence = %#v, want exact overloaded result", index, value)
		}
	}
	if value, ok := got[2]; !ok || !value.Active || value.ServerOverloaded {
		t.Fatalf("near-match target evidence = %#v, want active non-overloaded result", value)
	}
}

func TestRecentProxyOverloadEvidenceSplitsFiveHundredTargetBatches(t *testing.T) {
	targets := make([]LatestAccountRequestQuery, 1001)
	for i := range targets {
		targets[i] = LatestAccountRequestQuery{RequestIndex: i, AuthFileSnapshot: fmt.Sprintf("cred-%04d.json", i)}
	}
	batches := proxyOverloadTargetBatches(targets)
	if got, want := len(batches), 3; got != want {
		t.Fatalf("batch count = %d, want %d", got, want)
	}
	for index, want := range []int{500, 500, 1} {
		if got := len(batches[index]); got != want {
			t.Fatalf("batch %d size = %d, want %d", index, got, want)
		}
	}
	if batches[0][0].RequestIndex != 0 || batches[2][0].RequestIndex != 1000 {
		t.Fatalf("target order changed: first=%d last=%d", batches[0][0].RequestIndex, batches[2][0].RequestIndex)
	}
}

func TestRecentProxyOverloadEvidenceQueryPlanUsesLatestRequestIndexes(t *testing.T) {
	ctx := context.Background()
	db := openProxyOverloadTestDB(t, true)
	target := LatestAccountRequestQuery{
		RequestIndex:     0,
		AuthFileSnapshot: "plan.json",
		AuthIndex:        "idx-plan",
	}
	cutoffMS := time.Now().Add(-72 * time.Hour).UnixMilli()
	statement := "explain query plan " + proxyOverloadEvidenceStatement([]string{"(?, ?, ?, ?, ?, ?, ?, ?)"})
	planRows, err := db.QueryContext(ctx, statement,
		target.RequestIndex,
		target.AuthFileSnapshot,
		target.AuthIndex,
		"", "", "", "", 0,
		cutoffMS, cutoffMS, proxyOverloadEventLimit,
	)
	if err != nil {
		t.Fatalf("explain bounded evidence query: %v", err)
	}
	defer planRows.Close()
	var details []string
	for planRows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := planRows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	if !strings.Contains(strings.Join(details, "\n"), "idx_usage_events_latest_request_auth_file") {
		t.Fatalf("bounded evidence query plan did not use latest-request auth-file index: %v", details)
	}
}
func TestRecentProxyOverloadEvidenceFailsClosedWhenLatestRequestIndexesAreMissing(t *testing.T) {
	ctx := context.Background()
	db := openProxyOverloadTestDB(t, false)
	repo := New(db)

	_, err := repo.RecentProxyOverloadEvidence(ctx, []LatestAccountRequestQuery{{
		RequestIndex: 0, AuthFileSnapshot: "missing-index.json", AuthIndex: "idx",
	}}, time.Now().Add(-72*time.Hour).UnixMilli(), 9)
	if !errors.Is(err, ErrLatestRequestIndexesUnavailable) {
		t.Fatalf("error = %v, want ErrLatestRequestIndexesUnavailable", err)
	}
}

func TestRecentProxyOverloadEvidenceKeepsCodexIdentityFailClosedAndDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	db := openProxyOverloadTestDB(t, true)
	repo := New(db)
	nowMS := time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC).UnixMilli()

	event := latestAccountRequestEvent("proxy-overload-codex-event", nowMS, "shared.json", "idx", "shared.json")
	event.Provider = "codex"
	event.AuthProviderSnapshot = "codex"
	event.AuthAccountIDSnapshot = "workspace-1"
	event.AccountSnapshot = "alice@example.com"
	event.Failed = true
	event.FailStatusCode = 502
	event.HeaderErrorCode = "server_is_overloaded"
	if _, err := repo.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	var beforeRows int
	var beforeSchema string
	if err := db.QueryRow("select count(*) from usage_events").Scan(&beforeRows); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if err := db.QueryRow("select sql from sqlite_master where type = 'table' and name = 'usage_events'").Scan(&beforeSchema); err != nil {
		t.Fatalf("schema before: %v", err)
	}

	evidence, err := repo.RecentProxyOverloadEvidence(ctx, []LatestAccountRequestQuery{{
		RequestIndex:          0,
		AuthFileSnapshot:      "shared.json",
		AuthIndex:             "idx",
		Provider:              "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AuthProjectIDSnapshot: "codex-account-id:v1:workspace-2",
		AccountSnapshot:       "alice@example.com",
	}}, nowMS-int64(time.Hour/time.Millisecond), 9)
	if err != nil {
		t.Fatalf("recent proxy overload evidence: %v", err)
	}
	if len(evidence) != 0 {
		t.Fatalf("conflicting Codex target produced evidence: %#v", evidence)
	}

	var afterRows int
	var afterSchema string
	if err := db.QueryRow("select count(*) from usage_events").Scan(&afterRows); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if err := db.QueryRow("select sql from sqlite_master where type = 'table' and name = 'usage_events'").Scan(&afterSchema); err != nil {
		t.Fatalf("schema after: %v", err)
	}
	if afterRows != beforeRows || afterSchema != beforeSchema {
		t.Fatalf("read altered data/schema: rows %d -> %d schemaEqual=%t", beforeRows, afterRows, beforeSchema == afterSchema)
	}
}

func openProxyOverloadTestDB(t *testing.T, withIndexes bool) *sql.DB {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if withIndexes {
		if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
			t.Fatalf("prepare latest-request indexes: %v", err)
		}
	}
	return db
}

func evidenceByRequestIndex(evidence []ProxyOverloadEvidence) map[int]ProxyOverloadEvidence {
	result := make(map[int]ProxyOverloadEvidence, len(evidence))
	for _, item := range evidence {
		result[item.RequestIndex] = item
	}
	return result
}
