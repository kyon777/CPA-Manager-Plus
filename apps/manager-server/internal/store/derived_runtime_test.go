package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSeparateDerivedRuntimeServesAccountSnapshotFromDerivedDB(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	_, err = db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-1', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`)
	if err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run separate derived cycle: %v", err)
	}
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load separate snapshot: %v", err)
	}
	if !snapshot.Available || len(snapshot.CoreRows) == 0 || len(snapshot.Rows) == 0 {
		t.Fatalf("separate snapshot = %#v", snapshot)
	}
}

func TestSeparateDerivedRuntimeReturnsLastVerifiedRowsWhileTailIsPending(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	insertEvent := func(hash string, timestamp int64) {
		t.Helper()
		_, err := db.Exec(`insert into usage_events(
			event_hash, timestamp_ms, timestamp, provider, model, requested_model,
			resolved_model, auth_index, source, auth_file_snapshot,
			auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
			failed, created_at_ms
		) values (?, ?, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
			'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
			'codex', 10, 2, 12, 0, ?)`, hash, timestamp, timestamp)
		if err != nil {
			t.Fatalf("seed usage event %s: %v", hash, err)
		}
	}
	insertEvent("evt-1", 100)
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run initial separate derived cycle: %v", err)
	}
	insertEvent("evt-2", 200)
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load pending separate snapshot: %v", err)
	}
	if snapshot.Available {
		t.Fatal("pending separate snapshot reported available")
	}
	if len(snapshot.Rows) == 0 || len(snapshot.CoreRows) == 0 {
		t.Fatalf("pending separate snapshot discarded verified rows: %#v", snapshot)
	}
}

func TestSeparateDerivedRuntimeMergesVisibleAccountTailBeforeDerivedCatchUp(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	insertEvent := func(hash, authIndex, source string, timestamp int64) {
		t.Helper()
		_, err := db.Exec(`insert into usage_events(
			event_hash, timestamp_ms, timestamp, provider, model, requested_model,
			resolved_model, auth_index, source, auth_file_snapshot,
			auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
			failed, created_at_ms
		) values (?, ?, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
			'gpt-test', 'gpt-test', ?, ?, ?, 'codex', 10, 2, 12, 0, ?)`,
			hash, timestamp, authIndex, source, source, timestamp)
		if err != nil {
			t.Fatalf("seed usage event %s: %v", hash, err)
		}
	}
	insertEvent("evt-1", "idx-1", "credential.json", 100)
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run initial separate derived cycle: %v", err)
	}
	// The second event belongs to another credential and must not turn this
	// current-page lookup into a global tail scan. The third event belongs to
	// the visible credential and must be merged before the worker catches up.
	insertEvent("evt-other", "idx-other", "other.json", 200)
	insertEvent("evt-2", "idx-1", "credential.json", 300)

	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load separate snapshot with pending tail: %v", err)
	}
	if snapshot.Available {
		t.Fatal("tail-merged snapshot reported full derived coverage")
	}
	if len(snapshot.CoreRows) != 1 || snapshot.CoreRows[0].Calls != 2 || snapshot.CoreRows[0].TotalTokens != 24 {
		t.Fatalf("core rows did not include visible account tail: %#v", snapshot.CoreRows)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Calls != 2 || snapshot.Rows[0].TotalTokens != 24 {
		t.Fatalf("pricing rows did not include visible account tail: %#v", snapshot.Rows)
	}
}

func TestSeparateDerivedRuntimeMarksPricingSnapshotStaleAfterPriceRevisionChanges(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveModelPrices(context.Background(), map[string]ModelPrice{
		"gpt-test": {Prompt: 1, Completion: 1},
	}); err != nil {
		t.Fatalf("seed model price: %v", err)
	}
	_, err = db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-price-revision', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`)
	if err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run initial separate derived cycle: %v", err)
	}
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	initial, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load initial pricing snapshot: %v", err)
	}
	if !initial.Available || len(initial.Rows) != 1 {
		t.Fatalf("initial pricing snapshot = %#v, want available row", initial)
	}
	if err := st.SaveModelPrices(context.Background(), map[string]ModelPrice{
		"gpt-test": {Prompt: 2, Completion: 1},
	}); err != nil {
		t.Fatalf("change model price: %v", err)
	}
	stale, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load stale pricing snapshot: %v", err)
	}
	if stale.Available {
		t.Fatal("pricing snapshot remained available after the source price revision changed")
	}
	if len(stale.Rows) != 1 || stale.Rows[0].Calls != initial.Rows[0].Calls {
		t.Fatalf("price revision discarded last verified rows: %#v", stale.Rows)
	}
}

func TestSeparateDerivedRuntimeMarksSnapshotStaleWhenArchiveGenerationChanges(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	_, err = db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-archive-generation', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`)
	if err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run initial separate derived cycle: %v", err)
	}
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	initial, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil || !initial.Available {
		t.Fatalf("initial snapshot = %#v err=%v", initial, err)
	}
	if _, err := db.Exec(`insert into usage_archive_runs(
		id, mode, schema_version, format, status, cutoff_timestamp_ms,
		target_event_id, event_count, estimated_bytes, created_at_ms, updated_at_ms,
		completed_at_ms
	) values ('archive-generation-2', 'manual', 1, 'gzip-jsonl-v1', 'completed',
		1000, 1, 1, 1, 200, 200, 200)`); err != nil {
		t.Fatalf("seed completed archive generation: %v", err)
	}
	stale, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load stale archive snapshot: %v", err)
	}
	if stale.Available {
		t.Fatal("snapshot remained available after the source archive generation changed")
	}
	if len(stale.Rows) != 1 || len(stale.CoreRows) != 1 {
		t.Fatalf("archive generation change discarded verified rows: %#v", stale)
	}
}

func TestSeparateDerivedRuntimeFallsBackToVerifiedPrimaryRowsWhileWarming(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	_, err = db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-1', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`)
	if err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	if _, err := st.CatchUpAccountHistoryRollups(context.Background(), 10, 1000); err != nil {
		t.Fatalf("build primary account history: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(context.Background(), 10, 1000); err != nil {
		t.Fatalf("build primary pricing: %v", err)
	}
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load warming separate snapshot: %v", err)
	}
	if len(snapshot.Rows) == 0 || len(snapshot.CoreRows) == 0 {
		t.Fatalf("warming snapshot did not preserve primary values: %#v", snapshot)
	}
	if snapshot.Available {
		t.Fatal("warming separate snapshot reported fresh availability")
	}
}

func TestSeparateDerivedArchiveDeleteReadinessFailsClosedUntilAllRequiredStreamsExist(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	_, err = db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-1', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`)
	if err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(context.Background(), cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	ready, err := st.SeparateDerivedArchiveDeleteReady(context.Background(), 1)
	if err != nil {
		t.Fatalf("check warming archive readiness: %v", err)
	}
	if ready {
		t.Fatal("unmaterialized separate streams unexpectedly allowed archive deletion")
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run separate derived cycle: %v", err)
	}
	ready, err = st.SeparateDerivedArchiveDeleteReady(context.Background(), 1)
	if err != nil {
		t.Fatalf("check caught-up archive readiness: %v", err)
	}
	if ready {
		t.Fatal("separate storage allowed raw archive deletion before isolated hourly and monitoring projections exist")
	}
}

func TestSeparateDerivedRuntimeDefersLegacyDerivedCatchUps(t *testing.T) {
	ctx := context.Background()
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })

	// Complete the one-time raw-accounting migration before the event is
	// inserted. The legacy catch-up paths would otherwise return Pending for
	// the unrelated migration gate instead of proving that separate mode never
	// opens a primary-database derived write transaction.
	if _, err := st.DiscoverUsageCacheAccounting(ctx); err != nil {
		t.Fatalf("complete empty raw-accounting migration: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-separate-defer', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	if err := st.ConfigureDerivedStorage(ctx, config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}

	checks := []struct {
		name string
		run  func() (bool, error)
	}{
		{
			name: "hourly aggregate",
			run: func() (bool, error) {
				result, err := st.CatchUpUsageHourlyAggregate(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
		{
			name: "monitoring stats",
			run: func() (bool, error) {
				result, err := st.CatchUpUsageMonitoringStats(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
		{
			name: "monitoring projection",
			run: func() (bool, error) {
				result, err := st.CatchUpUsageMonitoringProjection(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
		{
			name: "monitoring metadata",
			run: func() (bool, error) {
				result, err := st.CatchUpUsageMonitoringMetadata(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
		{
			name: "codex legacy identity evidence",
			run: func() (bool, error) {
				result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
		{
			name: "dashboard hourly",
			run: func() (bool, error) {
				result, err := st.CatchUpDashboardHourlyRollups(ctx, 10, 1_000)
				return result.Pending, err
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			pending, err := check.run()
			if err != nil {
				t.Fatalf("catch-up: %v", err)
			}
			if !pending {
				t.Fatal("separate storage allowed a legacy primary-database derived catch-up")
			}
		})
	}
}
