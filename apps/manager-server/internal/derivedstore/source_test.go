package derivedstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

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
