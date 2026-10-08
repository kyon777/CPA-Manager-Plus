package usageprojection

import (
	"context"

	"database/sql"

	"errors"

	"path/filepath"

	"strings"

	"testing"

	_ "modernc.org/sqlite"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestSearchIndexLikePatternKeepsExactFallbackBoundary(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantPattern string
		wantOK      bool
	}{
		{name: "ordinary substring", query: " Trace-ABC ", wantPattern: "%trace-abc%", wantOK: true},
		{name: "three unicode characters", query: "中文测", wantPattern: "%中文测%", wantOK: true},
		{name: "one character", query: "a", wantOK: false},
		{name: "two characters", query: "ab", wantOK: false},
		{name: "two unicode characters", query: "中文", wantOK: false},
		{name: "percent wildcard", query: "trace%", wantOK: false},
		{name: "underscore wildcard", query: "trace_", wantOK: false},
		{name: "projection separator", query: "a\x1fb", wantOK: false},
		{name: "control character", query: "a\nb", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern, ok := SearchIndexLikePattern(test.query)
			if ok != test.wantOK || pattern != test.wantPattern {
				t.Fatalf("SearchIndexLikePattern(%q) = %q, %v; want %q, %v", test.query, pattern, ok, test.wantPattern, test.wantOK)
			}
		})
	}
}

func TestUpsertHeaderRangeKeepsQuotaSnapshotWhenNewerEventHasOnlyTraceMetadata(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "header-projection.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table usage_events (
			id integer primary key,
			event_hash text not null,
			timestamp_ms integer not null,
			provider text,
			auth_file_snapshot text,
			auth_index text,
			account_snapshot text,
			auth_label_snapshot text,
			auth_provider_snapshot text,
			auth_account_id_snapshot text,
			auth_project_id_snapshot text,
			source text,
			source_hash text,
			response_metadata_json text,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text,
			header_error_kind text,
			header_error_code text,
			header_trace_id text
		)`,
		`create table usage_monitoring_header_latest_v1 (
			snapshot_key text primary key,
			event_id integer not null,
			event_hash text not null,
			timestamp_ms integer not null,
			auth_file_snapshot text not null,
			auth_index text not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			auth_provider_snapshot text not null,
			auth_account_id_snapshot text not null,
			auth_project_id_snapshot text not null,
			source text not null,
			source_hash text not null,
			response_metadata_json text not null,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text not null,
			header_error_kind text not null,
			header_error_code text not null,
			header_trace_id text not null,
			updated_at_ms integer not null
		)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create projection test table: %v", err)
		}
	}

	insertEvent := `insert into usage_events (
		id, event_hash, timestamp_ms, provider, auth_file_snapshot, auth_index,
		account_snapshot, auth_label_snapshot, auth_provider_snapshot,
		auth_account_id_snapshot, auth_project_id_snapshot, source, source_hash,
		response_metadata_json, header_quota_recover_at_ms,
		header_quota_used_percent, header_quota_plan_type, header_error_kind,
		header_error_code, header_trace_id
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, insertEvent,
		1, "quota-event", 1_000, "codex", "accounts/demo.json", "0",
		"demo@example.test", "demo", "codex", "", "", "proxy", "source-a",
		`{"quota":{"credits_balance":42.5,"credits_has_credits":true}}`, nil,
		100.0, "free", "", "", "",
	); err != nil {
		t.Fatalf("insert quota event: %v", err)
	}
	if _, err := db.ExecContext(ctx, insertEvent,
		2, "trace-only-event", 2_000, "codex", "accounts/demo.json", "0",
		"demo@example.test", "demo", "codex", "", "", "proxy", "source-a",
		`{"routing":{"provider":"codex"},"trace":{"primary_trace_id":"unsupported-model"}}`, nil,
		nil, nil, "", "", "unsupported-model",
	); err != nil {
		t.Fatalf("insert trace-only event: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if err := UpsertHeaderRange(ctx, tx, 0, 2, 3_000); err != nil {
		_ = tx.Rollback()
		t.Fatalf("upsert header range: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	var eventID int64
	var metadata string
	var quotaUsed sql.NullFloat64
	if err := db.QueryRowContext(ctx, `select event_id, response_metadata_json, header_quota_used_percent from usage_monitoring_header_latest_v1`).Scan(&eventID, &metadata, &quotaUsed); err != nil {
		t.Fatalf("read projected header: %v", err)
	}
	if eventID != 1 {
		t.Fatalf("projected event_id = %d; want the prior quota-bearing event 1", eventID)
	}
	if !strings.Contains(metadata, `"credits_balance":42.5`) {
		t.Fatalf("projected metadata lost credits: %s", metadata)
	}
	if !quotaUsed.Valid || quotaUsed.Float64 != 100 {
		t.Fatalf("projected quota used = %#v; want 100", quotaUsed)
	}
}
func TestVerifyRetainedEdgeTx(t *testing.T) {
	ctx := context.Background()
	setupDB := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		schema := `
			create table usage_monitoring_rollup_state (
				rollup_name text primary key,
				schema_version int,
				structure_revision text,
				status text,
				coverage_event_id int
			);
			create table usage_archive_event_refs (
				raw_event_id int,
				raw_deleted_at_ms int,
				timestamp_ms int
			);
			create table usage_monitoring_event_projection_v1 (
				event_id int
			);
		`
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		return db
	}

	currentRevision := usageidentity.MonitoringProjectionStructureRevision()

	t.Run("missing projection state", func(t *testing.T) {
		db := setupDB(t)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("incompatible schema version", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 0, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("incompatible structure revision", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, 'obsolete', 'ready', 10)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("clearing status", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'clearing', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("missing deleted edge event in projection", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (5, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("edge event beyond coverage ID", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 5)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (10, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_monitoring_event_projection_v1 values (10)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("complete retained edge", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (5, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_monitoring_event_projection_v1 values (5)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("real SQL query error does not return ErrRetainedCoverageIncomplete", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`drop table usage_monitoring_rollup_state`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if err == nil {
			t.Fatal("expected error on dropped table, got nil")
		}
		if errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("SQL query error must not be classified as ErrRetainedCoverageIncomplete: %v", err)
		}
	})
}
