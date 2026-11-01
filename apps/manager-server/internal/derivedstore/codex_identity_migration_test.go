package derivedstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestMigrateCodexEmailIdentityFoldsLegacyMaterializationsWithoutReplay(t *testing.T) {
	ctx := context.Background()
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	snapshot := SourceSnapshot{
		Identity:                 SourceIdentity{Epoch: "source-epoch-a"},
		TargetEventID:            8,
		ArchiveGeneration:        "archive-g1",
		PricingStructureRevision: "structure-v1:fixture-prices",
		PricingValueRevision:     "values-v1:fixture-prices",
	}
	legacyGeneration := func(stream Stream) string {
		if stream == StreamPricing {
			return "pricing-v1:source-epoch-a:values-v1:fixture-prices"
		}
		return "account-history-v1:source-epoch-a"
	}
	canonicalKey, ok := usageidentity.AccountKey(usageidentity.Fields{
		AuthProviderSnapshot: "codex",
		AccountSnapshot:      "same@example.com",
	})
	if !ok {
		t.Fatal("derive canonical Codex email key")
	}

	seed := func(stream Stream) {
		t.Helper()
		generation := legacyGeneration(stream)
		base := AccountAggregate{
			AccountKey:           "legacy-import-key",
			AccountSnapshot:      "same@example.com",
			AuthLabelSnapshot:    "same@example.com",
			AuthProviderSnapshot: "codex",
			AuthIndex:            "before",
			Source:               "before.json",
			SourceHash:           "before-hash",
			Model:                "gpt-test",
			BillingModel:         "gpt-test",
			PricingModel:         "gpt-test",
			Calls:                3,
			SuccessCalls:         3,
			InputTokens:          30,
			TotalTokens:          30,
			FirstSeenMS:          1,
			LastSeenMS:           3,
			UpdatedAtMS:          3,
		}
		refreshed := base
		refreshed.AccountKey = "legacy-workspace-key"
		refreshed.AccountSnapshot = " SAME@EXAMPLE.COM "
		refreshed.AuthIndex = "after"
		refreshed.Source = "after.json"
		refreshed.SourceHash = "after-hash"
		refreshed.Calls = 5
		refreshed.SuccessCalls = 5
		refreshed.InputTokens = 50
		refreshed.TotalTokens = 50
		refreshed.FirstSeenMS = 2
		refreshed.LastSeenMS = 8
		refreshed.UpdatedAtMS = 8
		first, err := MaterializedRowForAggregate(stream, generation, base, 1, 3, 3)
		if err != nil {
			t.Fatalf("materialize first legacy aggregate: %v", err)
		}
		second, err := MaterializedRowForAggregate(stream, generation, refreshed, 4, 8, 8)
		if err != nil {
			t.Fatalf("materialize second legacy aggregate: %v", err)
		}
		if _, err := NewSink(db).Apply(ctx, ApplyBatch{
			Stream:                   stream,
			SourceEpoch:              snapshot.Identity.Epoch,
			Generation:               generation,
			PricingStructureRevision: snapshot.PricingStructureRevision,
			PricingValueRevision:     snapshot.PricingValueRevision,
			ArchiveGeneration:        snapshot.ArchiveGeneration,
			ToEventID:                snapshot.TargetEventID,
			SourceTargetEventID:      snapshot.TargetEventID,
			Events: []EventRecord{
				{ID: 1, EventHash: string(stream) + "-1"},
				{ID: 8, EventHash: string(stream) + "-8"},
			},
			Rows:        []MaterializedRow{first, second},
			AppliedAtMS: 9,
		}); err != nil {
			t.Fatalf("seed %s legacy materialization: %v", stream, err)
		}
	}
	seed(StreamAccountHistory)
	seed(StreamPricing)
	if _, err := db.Exec(`insert into derived_materialized_rows (
		stream_name, generation, row_key, subject_key, payload_json, first_event_id, last_event_id, updated_at_ms
	) values (?, 'orphan-generation', 'orphan', 'orphan', '{"calls":1}', 1, 1, 1)`, string(StreamPricing)); err != nil {
		t.Fatalf("seed stale orphan: %v", err)
	}
	if _, err := db.Exec(`insert into usage_pricing_account_price_cache_v1 (
		account_key, pricing_revision, covered_event_id, total_cost, updated_at_ms, status, last_error
	) values ('legacy-import-key', ?, 8, 1.5, 8, 'fresh', '')`, snapshot.PricingValueRevision); err != nil {
		t.Fatalf("seed derived price cache: %v", err)
	}

	migrated, err := MigrateCodexEmailIdentity(ctx, NewSink(db), snapshot)
	if err != nil {
		t.Fatalf("migrate legacy derived identity: %v", err)
	}
	if !migrated {
		t.Fatal("legacy derived identity migration was not reported")
	}

	for _, stream := range []Stream{StreamAccountHistory, StreamPricing} {
		generation := GenerationFor(stream, snapshot)
		state, err := NewSink(db).State(ctx, stream)
		if err != nil {
			t.Fatalf("read migrated %s state: %v", stream, err)
		}
		if state.Generation != generation || state.Status != "ready" ||
			state.CoverageEventID != 8 || state.TargetEventID != 8 ||
			state.ArchiveGeneration != snapshot.ArchiveGeneration ||
			state.PricingStructureRevision != snapshot.PricingStructureRevision ||
			state.PricingValueRevision != snapshot.PricingValueRevision ||
			state.LastTransition != "legacy_promotion" {
			t.Fatalf("migrated %s state = %#v", stream, state)
		}
		rows, err := NewSink(db).Rows(ctx, stream, generation)
		if err != nil {
			t.Fatalf("read migrated %s rows: %v", stream, err)
		}
		if len(rows) != 1 || rows[0].SubjectKey != canonicalKey {
			t.Fatalf("migrated %s rows = %#v", stream, rows)
		}
		var aggregate AccountAggregate
		if err := json.Unmarshal([]byte(rows[0].PayloadJSON), &aggregate); err != nil {
			t.Fatalf("decode migrated %s aggregate: %v", stream, err)
		}
		if aggregate.AccountKey != canonicalKey || aggregate.AccountSnapshot != "same@example.com" ||
			aggregate.Calls != 8 || aggregate.SuccessCalls != 8 || aggregate.TotalTokens != 80 ||
			aggregate.FirstSeenMS != 1 || aggregate.LastSeenMS != 8 {
			t.Fatalf("migrated %s aggregate = %#v", stream, aggregate)
		}
		oldRows, err := NewSink(db).Rows(ctx, stream, legacyGeneration(stream))
		if err != nil {
			t.Fatalf("read stale %s rows: %v", stream, err)
		}
		if len(oldRows) != 0 {
			t.Fatalf("stale %s materializations were retained: %#v", stream, oldRows)
		}
	}
	var staleRows, staleLedger, caches int
	if err := db.QueryRow(`select count(*) from derived_materialized_rows where generation = 'orphan-generation'`).Scan(&staleRows); err != nil {
		t.Fatalf("count orphan materializations: %v", err)
	}
	if err := db.QueryRow(`select count(*) from derived_applied_events where generation like '%source-epoch-a%'`).Scan(&staleLedger); err != nil {
		t.Fatalf("count stale derived ledgers: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_pricing_account_price_cache_v1`).Scan(&caches); err != nil {
		t.Fatalf("count derived price caches: %v", err)
	}
	if staleRows != 0 || staleLedger != 0 || caches != 0 {
		t.Fatalf("stale derived state = rows:%d ledgers:%d caches:%d", staleRows, staleLedger, caches)
	}
}
