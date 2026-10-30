package derivedstore

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestBoundedSubjectTailRangeLimitsPrimaryScan(t *testing.T) {
	tests := []struct {
		name       string
		afterID    int64
		targetID   int64
		wantAfter  int64
		wantTarget int64
	}{
		{name: "large backlog", afterID: 0, targetID: 100_000, wantAfter: 100_000 - subjectTailScanLimit, wantTarget: 100_000},
		{name: "short backlog", afterID: 80, targetID: 100, wantAfter: 80, wantTarget: 100},
		{name: "coverage newer than scan floor", afterID: 90_000, targetID: 100_000, wantAfter: 90_000, wantTarget: 100_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotAfter, gotTarget, complete := boundedSubjectTailRange(test.afterID, test.targetID)
			if gotAfter != test.wantAfter || gotTarget != test.wantTarget {
				t.Fatalf("bounded subject tail range = (%d, %d), want (%d, %d)", gotAfter, gotTarget, test.wantAfter, test.wantTarget)
			}
			wantComplete := gotAfter == test.afterID
			if complete != wantComplete {
				t.Fatalf("bounded subject tail completeness = %t, want %t", complete, wantComplete)
			}
			if gotTarget-gotAfter > subjectTailScanLimit {
				t.Fatalf("bounded subject tail span = %d, exceeds %d", gotTarget-gotAfter, subjectTailScanLimit)
			}
		})
	}
}

func TestSourceLoadsModelPricesWithContextAndServiceTiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	_, err = db.Exec(`insert into model_prices(
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m,
		cache_creation_per_1m, prompt_configured, completion_configured,
		cache_read_configured, cache_creation_configured, source, source_model_id,
		raw_json, updated_at_ms, synced_at_ms
	) values ('gpt-test', 1.25, 2.5, 0.1, 0.05, 0.2, 1, 1, 1, 1,
		'manual', 'gpt-test', '{}', 11, 12)`)
	if err != nil {
		t.Fatalf("seed model price: %v", err)
	}
	_, err = db.Exec(`insert into model_price_context_tiers(
		model, threshold_tokens, prompt_per_1m, completion_per_1m, cache_per_1m,
		cache_read_per_1m, cache_creation_per_1m, prompt_configured,
		completion_configured, cache_configured, cache_read_configured,
		cache_creation_configured
	) values ('gpt-test', 272000, 3, 4, 0.3, 0.15, 0.4, 1, 1, 1, 1, 1)`)
	if err != nil {
		t.Fatalf("seed context tier: %v", err)
	}
	_, err = db.Exec(`insert into model_price_service_tiers(
		model, mode, service_tier, prompt_per_1m, completion_per_1m,
		cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_configured,
		cache_read_configured, cache_creation_configured
	) values ('gpt-test', 'fast', 'priority', 5, 6, 0.5, 0.25, 0.6, 1, 1, 1, 1, 1)`)
	if err != nil {
		t.Fatalf("seed service tier: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close primary writer: %v", err)
	}
	ro, err := sqliterepo.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open primary source: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	prices, err := NewSource(ro).ModelPrices(context.Background())
	if err != nil {
		t.Fatalf("load model prices: %v", err)
	}
	price, ok := prices["gpt-test"]
	if !ok || price.Prompt != 1.25 || len(price.ContextTiers) != 1 || len(price.ServiceTiers) != 1 {
		t.Fatalf("model price snapshot = %#v", prices)
	}
	if price.ContextTiers[0].ThresholdTokens != 272000 || price.ServiceTiers[0].ServiceTier != "priority" {
		t.Fatalf("model price tiers = %#v", price)
	}
}

func TestSourceReadsShortBoundedBatchWithoutWritingPrimary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	seedUsageEvent(t, db, "event-1", 1)
	seedUsageEvent(t, db, "event-2", 2)
	seedUsageEvent(t, db, "event-3", 3)
	var beforeEvents, beforeLedger, beforeSettings int
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&beforeEvents); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_event_identity_ledger`).Scan(&beforeLedger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if err := db.QueryRow(`select count(*) from settings`).Scan(&beforeSettings); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close primary writer: %v", err)
	}
	readOnly, err := sqliterepo.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("open primary source: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	source := NewSource(readOnly)
	got, err := source.EventsAfter(context.Background(), 0, 3, 2)
	if err != nil {
		t.Fatalf("read bounded source batch: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("bounded events = %#v", got)
	}
	if err := readOnly.QueryRow(`select count(*) from usage_events`).Scan(&beforeEvents); err != nil {
		t.Fatalf("recount events: %v", err)
	}
	var afterLedger, afterSettings int
	if err := readOnly.QueryRow(`select count(*) from usage_event_identity_ledger`).Scan(&afterLedger); err != nil {
		t.Fatalf("recount ledger: %v", err)
	}
	if err := readOnly.QueryRow(`select count(*) from settings`).Scan(&afterSettings); err != nil {
		t.Fatalf("recount settings: %v", err)
	}
	if beforeEvents != 3 || afterLedger != beforeLedger || afterSettings != beforeSettings {
		t.Fatalf("primary changed during source read: events=%d ledger=%d settings=%d", beforeEvents, afterLedger, afterSettings)
	}
}

func TestSourceReadsDeletedArchivedEventsFromBoundedSegment(t *testing.T) {
	ctx := context.Background()
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	archiveDir := filepath.Join(t.TempDir(), "usage-archives")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	seedDeletedArchiveSegment(t, db, archiveDir, 7, strings.Repeat("a", 64), 100)
	if err := db.Close(); err != nil {
		t.Fatalf("close primary writer: %v", err)
	}
	readOnly, err := sqliterepo.OpenReadOnly(primaryPath)
	if err != nil {
		t.Fatalf("open primary source: %v", err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })

	source := NewSourceWithArchiveDir(readOnly, archiveDir)
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatalf("read source snapshot: %v", err)
	}
	if snapshot.TargetEventID != 7 || snapshot.ArchiveGeneration != "run-archive" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	got, err := source.ArchivedEventsAfter(ctx, 0, snapshot.TargetEventID, 1)
	if err != nil {
		t.Fatalf("read archived source batch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("archived events = %#v", got)
	}
	if event := got[0]; event.ID != 7 || event.EventHash != strings.Repeat("a", 64) ||
		event.AuthFileSnapshot != "archived.json" || event.InputTokens != 10 || event.TotalTokens != 12 {
		t.Fatalf("archived event = %#v", event)
	}
}

func seedDeletedArchiveSegment(t *testing.T, db *sql.DB, archiveDir string, eventID int64, eventHash string, timestampMS int64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"_cpamp_archive_schema_version":    usage.ArchiveSchemaVersion,
		"_cpamp_archive_event_id":          eventID,
		"event_hash":                       eventHash,
		"timestamp_ms":                     timestampMS,
		"timestamp":                        "1970-01-01T00:00:00.100Z",
		"provider":                         "codex",
		"model":                            "gpt-test",
		"requested_model":                  "gpt-test",
		"auth_index":                       "archive-index",
		"source":                           "archived.json",
		"auth_file_snapshot":               "archived.json",
		"auth_provider_snapshot":           "codex",
		"input_tokens":                     int64(10),
		"output_tokens":                    int64(2),
		"reasoning_tokens":                 int64(0),
		"cached_tokens":                    int64(0),
		"cache_tokens":                     int64(0),
		"cache_read_tokens":                int64(0),
		"cache_creation_tokens":            int64(0),
		"cache_input_mode":                 usage.CacheInputModeIncluded,
		"normalized_uncached_input_tokens": int64(10),
		"normalized_total_input_tokens":    int64(10),
		"normalized_cache_read_tokens":     int64(0),
		"normalized_cache_creation_tokens": int64(0),
		"total_tokens":                     int64(12),
		"failed":                           int64(0),
		"fail_status_code":                 int64(0),
		"fail_summary":                     "",
		"fail_body":                        "",
		"header_quota_recover_at_ms":       int64(0),
		"header_quota_used_percent":        nil,
		"raw_json":                         "",
		"created_at_ms":                    timestampMS,
	})
	if err != nil {
		t.Fatalf("encode archive payload: %v", err)
	}
	parsed, err := usage.ParseImportPayload(payload)
	if err != nil || len(parsed.Events) != 1 || !parsed.Events[0].PreserveArchiveDerivedFields {
		t.Fatalf("archive fixture is not restorable: result=%#v err=%v", parsed, err)
	}
	const runID = "run-archive"
	fileName := filepath.ToSlash(filepath.Join(runID, "segment-000001-00000000000000000007-00000000000000000007.jsonl.gz"))
	path := filepath.Join(archiveDir, filepath.FromSlash(fileName))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create archive directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create archive segment: %v", err)
	}
	zipper := gzip.NewWriter(file)
	if _, err := zipper.Write(append(payload, '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("write archive segment: %v", err)
	}
	if err := zipper.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("close archive gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive segment: %v", err)
	}

	if _, err := db.Exec(`insert into usage_archive_runs (
		id, mode, schema_version, format, status, cutoff_timestamp_ms, target_event_id,
		event_count, estimated_bytes, last_archived_event_id, archived_event_count,
		archived_uncompressed_bytes, archived_compressed_bytes, last_deleted_event_id,
		deleted_event_count, created_at_ms, updated_at_ms, completed_at_ms
	) values (?, 'manual', ?, 'gzip-jsonl-v1', 'completed', ?, ?, 1, 0, ?, 1, 1, 1, ?, 1, 1, 1, 1)`,
		runID, usage.ArchiveSchemaVersion, timestampMS+1, eventID, eventID, eventID); err != nil {
		t.Fatalf("seed archive run: %v", err)
	}
	if _, err := db.Exec(`insert into usage_archive_segments (
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms, verified_at_ms
	) values (?, 1, 'verified', ?, ?, ?, ?, ?, 1, 1, 1, 'fixture', 'fixture', 1, 1)`,
		runID, fileName, eventID, eventID, timestampMS, timestampMS); err != nil {
		t.Fatalf("seed archive segment metadata: %v", err)
	}
	if _, err := db.Exec(`insert into usage_event_identity_ledger (
		event_hash, raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version,
		aggregate_structure_revision, first_seen_at_ms, updated_at_ms
	) values (?, null, ?, ?, 0, '', 1, 1)`, eventHash, timestampMS, timestampMS-(timestampMS%3600000)); err != nil {
		t.Fatalf("seed archive identity: %v", err)
	}
	if _, err := db.Exec(`insert into usage_archive_event_refs (
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values (?, ?, 1, ?, ?, 1, 2)`, eventHash, runID, eventID, timestampMS); err != nil {
		t.Fatalf("seed archive reference: %v", err)
	}
}

func seedUsageEvent(t *testing.T, db *sql.DB, hash string, id int64) {
	t.Helper()
	// This helper is intentionally kept local to the package test; the event
	// fields are the minimal immutable source needed by the bounded reader.
	_, err := db.Exec(`insert into usage_events(event_hash, timestamp_ms, timestamp, model, input_tokens, output_tokens, total_tokens, created_at_ms)
		values (?, ?, ?, 'gpt-test', 10, 2, 12, ?)`, hash, id, id, id)
	if err != nil {
		t.Fatalf("seed event %s: %v", hash, err)
	}
}
