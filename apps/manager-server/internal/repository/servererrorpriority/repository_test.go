package servererrorpriority

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestMigrationCreatesPriorityQueueWithoutChangingUsageEvents(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	insertUsageEvent(t, db, eligible502("existing-event"))
	if err := sqlite.Migrate(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	var eventCount int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("usage event count = %d, want 1", eventCount)
	}
	for _, table := range []string{
		"server_error_priority_demotion_jobs",
		"server_error_priority_demotion_cursor",
		"server_error_priority_demotion_enabled_intervals",
	} {
		assertTableExists(t, db, table)
	}
}

func TestBootstrapDoesNotReplayEventsBeforeFloor(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := New(db)
	insertUsageEvent(t, db, eligible502("old-event"))
	if err := repo.Bootstrap(ctx, true, 100); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	result, err := repo.Reconcile(ctx, 100, 101)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Scanned != 0 {
		t.Fatalf("scanned = %d, want 0", result.Scanned)
	}
	assertJobHashes(t, db)
}

func TestDisabledIntervalIsNeverBackfilledAfterReenable(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := New(db)
	if err := repo.Bootstrap(ctx, true, 1); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	insertUsageEvent(t, db, eligible502("enabled-one"))
	if err := repo.SetEnabled(ctx, false, 2); err != nil {
		t.Fatalf("disable: %v", err)
	}
	insertUsageEvent(t, db, eligible502("disabled"))
	if err := repo.SetEnabled(ctx, true, 3); err != nil {
		t.Fatalf("enable: %v", err)
	}
	insertUsageEvent(t, db, eligible502("enabled-two"))
	if _, err := repo.Reconcile(ctx, 100, 4); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertJobHashes(t, db, "enabled-one", "enabled-two")
}

func TestEnqueueKeepsTenUniqueEventsAsTenJobs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := New(db)
	if err := repo.Bootstrap(ctx, true, 1); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	events := make([]usage.Event, 0, 10)
	for index := 0; index < 10; index++ {
		event := eligible502(fmt.Sprintf("event-%d", index))
		insertUsageEvent(t, db, event)
		events = append(events, event)
	}
	first, err := repo.EnqueueEvents(ctx, events, 2)
	if err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if first.Admitted != 10 || first.Duplicate != 0 {
		t.Fatalf("first enqueue result = %#v", first)
	}
	second, err := repo.EnqueueEvents(ctx, events, 3)
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if second.Admitted != 0 || second.Duplicate != 10 {
		t.Fatalf("second enqueue result = %#v", second)
	}
	assertJobHashes(t, db,
		"event-0", "event-1", "event-2", "event-3", "event-4",
		"event-5", "event-6", "event-7", "event-8", "event-9",
	)
	var distinctIDs, count int
	if err := db.QueryRowContext(ctx, `select count(distinct id), count(*) from server_error_priority_demotion_jobs`).Scan(&distinctIDs, &count); err != nil {
		t.Fatalf("count job IDs: %v", err)
	}
	if distinctIDs != 10 || count != 10 {
		t.Fatalf("job IDs/count = %d/%d, want 10/10", distinctIDs, count)
	}
}

func TestEligibleMissingIdentityIsRetainedAsIgnoredJob(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := New(db)
	if err := repo.Bootstrap(ctx, true, 1); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	event := usage.Event{EventHash: "no-identity", Failed: true, FailStatusCode: http.StatusBadGateway, TimestampMS: 10}
	insertUsageEvent(t, db, event)
	result, err := repo.EnqueueEvents(ctx, []usage.Event{event}, 2)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if result.Ignored != 1 {
		t.Fatalf("result = %#v", result)
	}
	var state, reason string
	if err := db.QueryRowContext(ctx, `select state, coalesce(last_error_code, '') from server_error_priority_demotion_jobs where event_hash = ?`, event.EventHash).Scan(&state, &reason); err != nil {
		t.Fatalf("read ignored job: %v", err)
	}
	if state != "ignored" || reason != "credential_identity_missing" {
		t.Fatalf("ignored job = state:%q reason:%q", state, reason)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func eligible502(hash string) usage.Event {
	return usage.Event{
		EventHash:        hash,
		TimestampMS:      100,
		Timestamp:        "1970-01-01T00:00:00Z",
		Provider:         "codex",
		AuthIndex:        "auth-index",
		AccountSnapshot:  "member@example.com",
		AuthFileSnapshot: "credential.json",
		Failed:           true,
		FailStatusCode:   http.StatusBadGateway,
		Model:            "gpt-5",
	}
}

func insertUsageEvent(t *testing.T, db *sql.DB, event usage.Event) {
	t.Helper()
	if event.TimestampMS == 0 {
		event.TimestampMS = 100
	}
	if event.Timestamp == "" {
		event.Timestamp = "1970-01-01T00:00:00Z"
	}
	if event.Model == "" {
		event.Model = "gpt-5"
	}
	_, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		account_snapshot, auth_file_snapshot, auth_provider_snapshot,
		auth_account_id_snapshot, failed, fail_status_code, fail_body, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventHash, event.TimestampMS, event.Timestamp, event.Provider, event.Model, event.AuthIndex,
		event.AccountSnapshot, event.AuthFileSnapshot, event.AuthProviderSnapshot,
		event.AuthAccountIDSnapshot, boolToInt(event.Failed), nullableInt(event.FailStatusCode), nullableString(event.FailBody), event.TimestampMS,
	)
	if err != nil {
		t.Fatalf("insert usage event %q: %v", event.EventHash, err)
	}
}

func assertJobHashes(t *testing.T, db *sql.DB, want ...string) {
	t.Helper()
	rows, err := db.Query(`select event_hash from server_error_priority_demotion_jobs order by event_hash`)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()
	got := make([]string, 0)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan job: %v", err)
		}
		got = append(got, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate jobs: %v", err)
	}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("job hashes = %v, want %v", got, want)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var name string
	err := db.QueryRow(`select name from sqlite_master where type = 'table' and name = ?`, table).Scan(&name)
	if err != nil {
		t.Fatalf("table %q does not exist: %v", table, err)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
