package servererrorpriority

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestMigrationCreatesPriorityQueueWithoutChangingUsageEvents(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values (?, ?, ?, ?, ?)`, "existing-event", 1, "1970-01-01T00:00:00Z", "model", 1); err != nil {
		t.Fatalf("insert existing usage event: %v", err)
	}
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

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var name string
	err := db.QueryRow(`select name from sqlite_master where type = 'table' and name = ?`, table).Scan(&name)
	if err != nil {
		t.Fatalf("table %q does not exist: %v", table, err)
	}
}
