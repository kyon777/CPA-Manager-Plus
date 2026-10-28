package usagepricing

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestAccountPriceCacheReadUpsertAndFailurePreserveCost(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "price-cache.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	repo := New(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into usage_pricing_account_price_cache_v1
		(account_key, pricing_revision, covered_event_id, total_cost, updated_at_ms, status, last_error)
		values ('a', 'old', 10, 1.25, 100, 'fresh', ''),
		       ('a', 'new', 20, 2.50, 200, 'stale', 'rebuild'),
		       ('b', 'only', 30, 3.75, 150, 'fresh', '')`); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	rows, err := repo.LoadAccountPriceCacheTx(ctx, tx, []string{"a", "b", "missing"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("load cache: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit read tx: %v", err)
	}
	if len(rows) != 2 || rows[0].AccountKey != "a" || rows[0].PricingRevision != "new" {
		t.Fatalf("newest cache rows = %#v", rows)
	}
	if err := repo.UpsertAccountPriceCache(ctx, []PriceCacheRow{{
		AccountKey: "a", PricingRevision: "new", CoveredEventID: 25,
		TotalCost: 4.25, UpdatedAtMS: 300, Status: "fresh",
	}}); err != nil {
		t.Fatalf("upsert cache: %v", err)
	}
	var cost float64
	if err := db.QueryRowContext(ctx, `select total_cost from usage_pricing_account_price_cache_v1 where account_key = 'a' and pricing_revision = 'new'`).Scan(&cost); err != nil {
		t.Fatalf("read upserted cache: %v", err)
	}
	if cost != 4.25 {
		t.Fatalf("upserted cost=%v, want 4.25", cost)
	}
	if err := repo.MarkAccountPriceCacheFailure(ctx, []string{"a"}, "new", "busy", 400); err != nil {
		t.Fatalf("mark cache failure: %v", err)
	}
	var status, lastError string
	if err := db.QueryRowContext(ctx, `select total_cost, status, last_error from usage_pricing_account_price_cache_v1 where account_key = 'a' and pricing_revision = 'new'`).Scan(&cost, &status, &lastError); err != nil {
		t.Fatalf("read failed cache: %v", err)
	}
	if cost != 4.25 || status != "failed" || lastError != "busy" {
		t.Fatalf("failed cache cost=%v status=%q error=%q", cost, status, lastError)
	}
}
