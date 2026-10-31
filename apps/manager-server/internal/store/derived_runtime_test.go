package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/derivedstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
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

func TestSeparateDerivedRuntimeDegradesWithoutReenablingPrimaryWriter(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	// Point the derived path at the primary database. The guard must keep the
	// service alive in degraded separate mode rather than falling back to a
	// primary derived writer.
	if err := st.ConfigureDerivedStorage(context.Background(), config.Config{
		DBPath:             primaryPath,
		DerivedDBPath:      primaryPath,
		DerivedStorageMode: config.DerivedStorageSeparate,
	}); err != nil {
		t.Fatalf("degraded configuration returned fatal error: %v", err)
	}
	if !st.SeparateDerivedEnabled() || st.SeparateDerivedRuntimeAvailable() {
		t.Fatalf("separate degraded state = enabled:%v runtime:%v", st.SeparateDerivedEnabled(), st.SeparateDerivedRuntimeAvailable())
	}
	status, err := st.SeparateDerivedStatus(context.Background())
	if err != nil || len(status.Streams) != 1 || status.Streams[0].Status != "degraded" {
		t.Fatalf("degraded status = %#v, err=%v", status, err)
	}
	catchUp, err := st.CatchUpAccountHistoryRollups(context.Background(), 10, 1000)
	if err != nil {
		t.Fatalf("degraded account-history catch-up: %v", err)
	}
	if !catchUp.Pending {
		t.Fatalf("degraded account-history catch-up = %#v, want Pending=true", catchUp)
	}
}

func TestSeparateDerivedRuntimeDoesNotWritePrimaryRollups(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	insertEvent := func(hash string, timestamp int64, input, output int64) {
		t.Helper()
		_, err := db.Exec(`insert into usage_events(
			event_hash, timestamp_ms, timestamp, provider, model, requested_model,
			resolved_model, auth_index, source, auth_file_snapshot,
			auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
			failed, created_at_ms
		) values (?, ?, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
			'gpt-test', 'gpt-test', 'idx-primary-isolation', 'credential-isolation.json',
			'credential-isolation.json', 'codex', ?, ?, ?, 0, ?)`,
			hash, timestamp, input, output, input+output, timestamp)
		if err != nil {
			t.Fatalf("seed usage event %s: %v", hash, err)
		}
	}
	insertEvent("evt-primary-isolation-1", 100, 10, 2)
	if _, err := st.CatchUpAccountHistoryRollups(context.Background(), 10, 1000); err != nil {
		t.Fatalf("seed primary account history: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(context.Background(), 10, 1000); err != nil {
		t.Fatalf("seed primary pricing: %v", err)
	}
	var primaryHistoryCalls, primaryPricingCalls int64
	if err := db.QueryRow(`select coalesce(sum(calls), 0) from usage_account_model_rollups`).Scan(&primaryHistoryCalls); err != nil {
		t.Fatalf("read primary history before isolation: %v", err)
	}
	if err := db.QueryRow(`select coalesce(sum(calls), 0) from usage_pricing_account_rollups_v1`).Scan(&primaryPricingCalls); err != nil {
		t.Fatalf("read primary pricing before isolation: %v", err)
	}
	insertEvent("evt-primary-isolation-2", 200, 20, 4)
	derivedPath := filepath.Join(t.TempDir(), "usage-derived.sqlite")
	if err := st.ConfigureDerivedStorage(context.Background(), config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          derivedPath,
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run separate derived cycle: %v", err)
	}
	if err := db.QueryRow(`select coalesce(sum(calls), 0) from usage_account_model_rollups`).Scan(&primaryHistoryCalls); err != nil {
		t.Fatalf("read primary history after isolation: %v", err)
	}
	if err := db.QueryRow(`select coalesce(sum(calls), 0) from usage_pricing_account_rollups_v1`).Scan(&primaryPricingCalls); err != nil {
		t.Fatalf("read primary pricing after isolation: %v", err)
	}
	if primaryHistoryCalls != 1 || primaryPricingCalls != 1 {
		t.Fatalf("separate worker mutated primary rollups: history calls=%d pricing calls=%d", primaryHistoryCalls, primaryPricingCalls)
	}
	derivedDB, err := sqliterepo.OpenDerived(derivedPath)
	if err != nil {
		t.Fatalf("open derived database: %v", err)
	}
	t.Cleanup(func() { _ = derivedDB.Close() })
	var derivedHistoryCalls, derivedPricingCalls int64
	if err := derivedDB.QueryRow(`select coalesce(sum(json_extract(payload_json, '$.calls')), 0)
		from derived_materialized_rows where stream_name = ?`, derivedstore.StreamAccountHistory).Scan(&derivedHistoryCalls); err != nil {
		t.Fatalf("read derived history: %v", err)
	}
	if err := derivedDB.QueryRow(`select coalesce(sum(json_extract(payload_json, '$.calls')), 0)
		from derived_materialized_rows where stream_name = ?`, derivedstore.StreamPricing).Scan(&derivedPricingCalls); err != nil {
		t.Fatalf("read derived pricing: %v", err)
	}
	if derivedHistoryCalls != 2 || derivedPricingCalls != 2 {
		t.Fatalf("separate worker did not materialize both events: history calls=%d pricing calls=%d", derivedHistoryCalls, derivedPricingCalls)
	}
}

func TestSeparateDerivedRuntimeBootstrapsVerifiedPrimaryRowsBeforeSparseTail(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	insertEvent := func(hash string, timestamp int64, input, output int64) {
		t.Helper()
		_, err := db.Exec(`insert into usage_events(
			event_hash, timestamp_ms, timestamp, provider, model, requested_model,
			resolved_model, auth_index, source, auth_file_snapshot,
			auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
			failed, created_at_ms
		) values (?, ?, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
			'gpt-test', 'gpt-test', 'idx-bootstrap', 'bootstrap.json', 'bootstrap.json',
			'codex', ?, ?, ?, 0, ?)`,
			hash, timestamp, input, output, input+output, timestamp)
		if err != nil {
			t.Fatalf("seed usage event %s: %v", hash, err)
		}
	}
	insertEvent("evt-bootstrap-1", 100, 10, 2)
	if _, err := st.CatchUpAccountHistoryRollups(context.Background(), 10, 1000); err != nil {
		t.Fatalf("seed primary account history: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(context.Background(), 10, 1000); err != nil {
		t.Fatalf("seed primary pricing: %v", err)
	}
	// Simulate an already-archived/deleted first fact. The verified primary
	// rollups and their watermarks are the only safe baseline for a fresh
	// separate database in this sparse source.
	if _, err := db.Exec(`delete from usage_events where id = 1`); err != nil {
		t.Fatalf("remove archived raw event: %v", err)
	}
	insertEvent("evt-bootstrap-2", 200, 20, 4)
	derivedPath := filepath.Join(t.TempDir(), "usage-derived.sqlite")
	if err := st.ConfigureDerivedStorage(context.Background(), config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          derivedPath,
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(context.Background()); err != nil {
		t.Fatalf("run separate derived cycle from verified baseline: %v", err)
	}
	key := AccountHistoryKey("", "", "bootstrap.json", "idx-bootstrap")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load bootstrapped snapshot: %v", err)
	}
	if !snapshot.Available || len(snapshot.CoreRows) != 1 || snapshot.CoreRows[0].Calls != 2 {
		t.Fatalf("bootstrapped account history = %#v", snapshot.CoreRows)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].Calls != 2 {
		t.Fatalf("bootstrapped pricing rows = %#v", snapshot.Rows)
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

func TestSeparateDerivedSnapshotRecoversExactCurrentPagePricingWhenDerivedPricingLags(t *testing.T) {
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
		t.Fatalf("save model prices: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('exact-page-pricing', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 10, 2, 12, 0, 100)`); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	if _, err := st.CatchUpAccountHistoryRollups(context.Background(), 10, 1000); err != nil {
		t.Fatalf("build primary history: %v", err)
	}
	if _, err := st.CatchUpUsagePricing(context.Background(), 10, 1000); err != nil {
		t.Fatalf("build primary pricing: %v", err)
	}
	if err := st.ConfigureDerivedStorage(context.Background(), config.Config{
		DBPath: primaryPath, DerivedDBPath: filepath.Join(t.TempDir(), "usage-derived.sqlite"),
		DerivedStorageMode: config.DerivedStorageSeparate, DerivedSourceBatchSize: 10, DerivedCycleMaxBatches: 1,
	}); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	runtime := st.separateRuntime()
	if runtime == nil {
		t.Fatal("separate runtime unavailable")
	}
	// Keep the stream checkpoint at its verified primary target but remove its
	// local materialized row. This reproduces a pricing generation that has not
	// yet materialized the visible account while account-history is available.
	if _, err := runtime.derivedDB.Exec(`delete from derived_materialized_rows where stream_name = ?`, string(derivedstore.StreamPricing)); err != nil {
		t.Fatalf("remove derived pricing row: %v", err)
	}
	key := AccountHistoryKey("", "", "credential.json", "idx-1")
	snapshot, err := st.LoadUsagePricingAccountSnapshot(context.Background(), []string{key})
	if err != nil {
		t.Fatalf("load current-page snapshot: %v", err)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].AccountKey != key || snapshot.Rows[0].TotalTokens != 12 {
		t.Fatalf("current-page pricing rows = %#v", snapshot.Rows)
	}
	if _, incomplete := snapshot.PricingIncompleteAccountKeys[key]; incomplete {
		t.Fatalf("complete source pricing for %q was marked incomplete", key)
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

func TestSeparateDerivedRuntimeBoundsVisibleTailDuringLargeBacklog(t *testing.T) {
	template := derivedstore.EventRecord{
		Provider: "codex", AuthIndex: "idx-1", Source: "credential.json",
		AuthFileSnapshot: "credential.json", AuthProviderSnapshot: "codex",
		Model: "gpt-test", RequestedModel: "gpt-test", ResolvedModel: "gpt-test",
		InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
	}
	accountKey := derivedstore.AccountKeyForEvent(template)
	tail := make([]derivedstore.EventRecord, visibleAccountTailLimit+1)
	for index := range tail {
		event := template
		event.ID = 101 + int64(index)
		event.EventHash = fmt.Sprintf("tail-event-%d", event.ID)
		tail[index] = event
	}
	snapshot := derivedstore.SourceSnapshot{
		Identity:        derivedstore.SourceIdentity{Epoch: "source-epoch"},
		TargetEventID:   tail[len(tail)-1].ID,
		PricingRevision: "price-revision",
	}
	base := derivedstore.AccountAggregate{
		AccountKey: accountKey, Model: "gpt-test", BillingModel: "gpt-test",
		PricingModel: "gpt-test", Calls: 1, SuccessCalls: 1, TotalTokens: 12,
	}
	payload, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal aggregate: %v", err)
	}
	states := map[derivedstore.Stream]derivedstore.StreamState{
		derivedstore.StreamAccountHistory: {
			Stream: derivedstore.StreamAccountHistory, SourceEpoch: snapshot.Identity.Epoch,
			Generation: derivedstore.GenerationFor(derivedstore.StreamAccountHistory, snapshot),
			Status:     "ready", CoverageEventID: 100, TargetEventID: 100,
		},
		derivedstore.StreamPricing: {
			Stream: derivedstore.StreamPricing, SourceEpoch: snapshot.Identity.Epoch,
			Generation: derivedstore.GenerationFor(derivedstore.StreamPricing, snapshot),
			Status:     "ready", CoverageEventID: 100, TargetEventID: 100,
			PricingRevision: snapshot.PricingRevision,
		},
	}
	source := &runtimeTailCountingSource{
		snapshot: snapshot,
		tail:     tail,
	}
	runtime := &separateDerivedRuntime{
		source: source,
		sink: runtimeStaticSink{
			subjectKey: accountKey,
			states:     states,
			rows: map[derivedstore.Stream][]derivedstore.MaterializedRow{
				derivedstore.StreamAccountHistory: {{
					Stream:     derivedstore.StreamAccountHistory,
					Generation: states[derivedstore.StreamAccountHistory].Generation,
					Key:        "history-row", SubjectKey: accountKey, PayloadJSON: string(payload),
				}},
				derivedstore.StreamPricing: {{
					Stream:     derivedstore.StreamPricing,
					Generation: states[derivedstore.StreamPricing].Generation,
					Key:        "pricing-row", SubjectKey: accountKey, PayloadJSON: string(payload),
				}},
			},
		},
	}

	result, ready, err := runtime.loadAccountSnapshot(context.Background(), []string{accountKey}, map[string]ModelPrice{})
	if err != nil {
		t.Fatalf("load account snapshot: %v", err)
	}
	if ready || result.Available {
		t.Fatal("large source backlog unexpectedly reported a fully caught-up snapshot")
	}
	if source.subjectTailCalls != 2 {
		t.Fatalf("large source backlog queried visible tail %d times; want one bounded query per stream", source.subjectTailCalls)
	}
	if len(source.subjectTailLimits) != 2 {
		t.Fatalf("large source backlog recorded %d tail limits; want one per stream", len(source.subjectTailLimits))
	}
	for _, limit := range source.subjectTailLimits {
		if limit != visibleAccountTailLimit+1 {
			t.Fatalf("visible tail limit = %d, want %d", limit, visibleAccountTailLimit+1)
		}
	}
	if len(result.CoreRows) != 1 || result.CoreRows[0].Calls != 1 || len(result.Rows) != 1 || result.Rows[0].Calls != 1 {
		t.Fatalf("large source backlog changed last verified account rows: %#v", result)
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

func TestSeparateDerivedRuntimeBootstrapsVerifiedPrimaryRowsWhileWarming(t *testing.T) {
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
	if !snapshot.Available {
		t.Fatal("verified primary rows were not bootstrapped into an available separate snapshot")
	}
}

func TestSeparateDerivedWarmupFallbackDoesNotWaitForPrimaryWriter(t *testing.T) {
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary: %v", err)
	}
	st := New(db)
	t.Cleanup(func() { _ = st.Close() })
	if _, err := db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('evt-warm-lock', 100, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-lock', 'credential-lock.json', 'credential-lock.json',
		'codex', 10, 2, 12, 0, 100)`); err != nil {
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
	writer, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin primary writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Rollback() })
	if _, err := writer.Exec(`insert or replace into settings(key, value, updated_at_ms) values ('derived-test-lock', '1', 1)`); err != nil {
		t.Fatalf("hold primary writer: %v", err)
	}
	key := AccountHistoryKey("", "", "credential-lock.json", "idx-lock")
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	snapshot, err := st.LoadUsagePricingAccountSnapshot(ctx, []string{key})
	if err != nil {
		t.Fatalf("load warm snapshot under primary writer: %v", err)
	}
	if len(snapshot.CoreRows) == 0 || len(snapshot.Rows) == 0 {
		t.Fatalf("warm snapshot lost known rows under primary writer: %#v", snapshot)
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

type runtimeTailCountingSource struct {
	snapshot          derivedstore.SourceSnapshot
	tail              []derivedstore.EventRecord
	subjectTailCalls  int
	subjectTailLimits []int
}

func (s *runtimeTailCountingSource) Identity(context.Context) (derivedstore.SourceIdentity, error) {
	return s.snapshot.Identity, nil
}

func (s *runtimeTailCountingSource) Snapshot(context.Context) (derivedstore.SourceSnapshot, error) {
	return s.snapshot, nil
}

func (s *runtimeTailCountingSource) EventsAfter(context.Context, int64, int64, int) ([]derivedstore.EventRecord, error) {
	return nil, errors.New("unexpected full source read")
}

func (s *runtimeTailCountingSource) EventsForSubjectsAfter(_ context.Context, _ []string, _ int64, _ int64, limit int) ([]derivedstore.EventRecord, error) {
	s.subjectTailCalls++
	s.subjectTailLimits = append(s.subjectTailLimits, limit)
	return s.tail, nil
}

func (s *runtimeTailCountingSource) ArchivedEventsAfter(context.Context, int64, int64, int) ([]derivedstore.EventRecord, error) {
	return nil, errors.New("unexpected archive source read")
}

func (s *runtimeTailCountingSource) PricingRevision(context.Context) (string, error) {
	return s.snapshot.PricingRevision, nil
}

func (s *runtimeTailCountingSource) ModelPrices(context.Context) (map[string]model.ModelPrice, error) {
	return map[string]model.ModelPrice{}, nil
}

type runtimeStaticSink struct {
	states     map[derivedstore.Stream]derivedstore.StreamState
	rows       map[derivedstore.Stream][]derivedstore.MaterializedRow
	subjectKey string
}

func (s runtimeStaticSink) State(_ context.Context, stream derivedstore.Stream) (derivedstore.StreamState, error) {
	return s.states[stream], nil
}

func (s runtimeStaticSink) Rows(context.Context, derivedstore.Stream, string) ([]derivedstore.MaterializedRow, error) {
	return nil, errors.New("unexpected unbounded materialized read")
}

func (s runtimeStaticSink) RowsByKeys(context.Context, derivedstore.Stream, string, []string) ([]derivedstore.MaterializedRow, error) {
	return nil, errors.New("unexpected key materialized read")
}

func (s runtimeStaticSink) RowsForSubjects(_ context.Context, stream derivedstore.Stream, _ string, subjects []string) ([]derivedstore.MaterializedRow, error) {
	if len(subjects) != 1 || subjects[0] != s.subjectKey {
		return nil, errors.New("unexpected account subjects")
	}
	return append([]derivedstore.MaterializedRow(nil), s.rows[stream]...), nil
}

func (s runtimeStaticSink) Apply(context.Context, derivedstore.ApplyBatch) (derivedstore.StreamState, error) {
	return derivedstore.StreamState{}, errors.New("unexpected derived write")
}

func (s runtimeStaticSink) Status(context.Context) (derivedstore.Status, error) {
	return derivedstore.Status{}, errors.New("unexpected derived status read")
}
