package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenDerivedDoesNotCreatePrimaryControlTables(t *testing.T) {
	derived, err := OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = derived.Close() })
	assertDerivedTableExists(t, derived, "derived_store_meta")
	assertDerivedTableExists(t, derived, "derived_stream_state")
	assertDerivedTableExists(t, derived, "derived_applied_events")
	assertDerivedTableExists(t, derived, "usage_pricing_account_rollups_v1")
	assertDerivedTableMissing(t, derived, "token_recovery_tasks")
	assertDerivedTableMissing(t, derived, "settings")
	assertDerivedTableMissing(t, derived, "usage_events")
	assertDerivedTableMissing(t, derived, "usage_event_identity_ledger")
	assertDerivedTableMissing(t, derived, "usage_archive_runs")
}

func TestMigrateDerivedIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-derived.sqlite")
	derived, err := OpenDerived(path)
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	if _, err := derived.Exec(`insert into derived_store_meta(singleton, source_epoch, schema_revision, created_at_ms, updated_at_ms) values (1, 'epoch-a', 'schema-a', 1, 1)`); err != nil {
		_ = derived.Close()
		t.Fatalf("seed derived metadata: %v", err)
	}
	if err := derived.Close(); err != nil {
		t.Fatalf("close derived: %v", err)
	}
	derived, err = OpenDerived(path)
	if err != nil {
		t.Fatalf("reopen derived: %v", err)
	}
	t.Cleanup(func() { _ = derived.Close() })
	var epoch string
	if err := derived.QueryRow(`select source_epoch from derived_store_meta where singleton = 1`).Scan(&epoch); err != nil {
		t.Fatalf("read metadata after repeat migration: %v", err)
	}
	if epoch != "epoch-a" {
		t.Fatalf("source epoch = %q, want epoch-a", epoch)
	}
}

func TestMigrateDerivedAddsPricingRevisionColumnsToExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-derived.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy derived database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table derived_stream_state (
		stream_name text primary key,
		source_epoch text not null default '',
		generation text not null default '',
		status text not null default 'pending',
		coverage_event_id integer not null default 0,
		target_event_id integer not null default 0,
		pricing_revision text not null default '',
		archive_generation text not null default '',
		processed_events integer not null default 0,
		last_success_at_ms integer,
		last_run_started_at_ms integer,
		last_batch_duration_ms integer not null default 0,
		backoff_count integer not null default 0,
		last_error_code text not null default '',
		updated_at_ms integer not null default 0
	)`); err != nil {
		t.Fatalf("seed legacy state schema: %v", err)
	}
	if err := MigrateDerived(db); err != nil {
		t.Fatalf("migrate legacy derived database: %v", err)
	}
	columns := map[string]bool{}
	rows, err := db.Query(`pragma table_info(derived_stream_state)`)
	if err != nil {
		t.Fatalf("inspect derived stream state columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan derived stream state column: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate derived stream state columns: %v", err)
	}
	for _, name := range []string{"pricing_revision", "pricing_structure_revision", "last_transition"} {
		if !columns[name] {
			t.Fatalf("missing derived stream state column %q: %#v", name, columns)
		}
	}
}

func assertDerivedTableExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("inspect table %q: %v", name, err)
	}
	if count != 1 {
		t.Fatalf("table %q count = %d, want 1", name, count)
	}
}

func assertDerivedTableMissing(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("inspect absent table %q: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("primary table %q exists in derived database", name)
	}
}

func TestOpenDerivedContainsIndependentHourlyAndMonitoringProjectionTables(t *testing.T) {
	derived, err := OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived: %v", err)
	}
	t.Cleanup(func() { _ = derived.Close() })

	for _, table := range []string{
		"usage_pricing_hourly_rollups_v1",
		"usage_dashboard_hourly_rollups",
		"usage_hourly_aggregate_v1",
		"derived_monitoring_account_daily_rollups_v1",
		"derived_monitoring_api_key_daily_rollups_v1",
		"derived_monitoring_header_latest_v1",
	} {
		assertDerivedTableExists(t, derived, table)
	}
}
