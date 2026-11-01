package derivedstore

import (
	"context"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestMigrateLegacyPricingGenerationPromotesCurrentCheckpoint(t *testing.T) {
	ctx := context.Background()
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshot := SourceSnapshot{
		Identity:                 SourceIdentity{Epoch: "epoch-a"},
		TargetEventID:            1000,
		ArchiveGeneration:        "archive-a",
		PricingStructureRevision: "structure-a",
		PricingValueRevision:     "value-a",
	}
	legacyGeneration := currentLegacyPricingGeneration(snapshot)
	sink := NewSink(db)
	if _, err := sink.Apply(ctx, ApplyBatch{
		Stream:               StreamPricing,
		SourceEpoch:          snapshot.Identity.Epoch,
		Generation:           legacyGeneration,
		PricingValueRevision: snapshot.PricingValueRevision,
		ArchiveGeneration:    snapshot.ArchiveGeneration,
		ToEventID:            812,
		SourceTargetEventID:  900,
		Events:               []EventRecord{{ID: 812, EventHash: "event-812"}},
		Rows: []MaterializedRow{{
			Key: "account-a", SubjectKey: "account-a", PayloadJSON: `{"calls":1}`,
		}},
	}); err != nil {
		t.Fatalf("seed legacy pricing generation: %v", err)
	}

	migrated, err := MigrateLegacyPricingGeneration(ctx, sink, snapshot)
	if err != nil || !migrated {
		t.Fatalf("promote current legacy generation = %v, %v", migrated, err)
	}
	state, err := sink.State(ctx, StreamPricing)
	if err != nil {
		t.Fatalf("read promoted pricing state: %v", err)
	}
	if state.Generation != GenerationFor(StreamPricing, snapshot) ||
		state.CoverageEventID != 812 || state.TargetEventID != 1000 || state.ProcessedEvents != 1 ||
		state.PricingStructureRevision != snapshot.PricingStructureRevision ||
		state.PricingValueRevision != snapshot.PricingValueRevision ||
		state.LastTransition != "legacy_promotion" {
		t.Fatalf("promoted pricing state = %#v", state)
	}
	rows, err := sink.Rows(ctx, StreamPricing, state.Generation)
	if err != nil {
		t.Fatalf("read promoted pricing rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "account-a" {
		t.Fatalf("promoted pricing rows = %#v", rows)
	}
	var legacyLedger int
	if err := db.QueryRow(`select count(*) from derived_applied_events where stream_name = ? and generation = ?`,
		string(StreamPricing), legacyGeneration).Scan(&legacyLedger); err != nil {
		t.Fatalf("count retained legacy ledger: %v", err)
	}
	if legacyLedger != 1 {
		t.Fatalf("legacy ledger was rewritten during promotion: %d", legacyLedger)
	}
}

func TestMigrateLegacyPricingGenerationRejectsDifferentValueRevision(t *testing.T) {
	ctx := context.Background()
	db, err := sqliterepo.OpenDerived(filepath.Join(t.TempDir(), "usage-derived.sqlite"))
	if err != nil {
		t.Fatalf("open derived database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	current := SourceSnapshot{
		Identity:                 SourceIdentity{Epoch: "epoch-a"},
		TargetEventID:            1000,
		ArchiveGeneration:        "archive-a",
		PricingStructureRevision: "structure-a",
		PricingValueRevision:     "value-current",
	}
	legacy := current
	legacy.PricingValueRevision = "value-old"
	legacyGeneration := currentLegacyPricingGeneration(legacy)
	sink := NewSink(db)
	if _, err := sink.Apply(ctx, ApplyBatch{
		Stream:               StreamPricing,
		SourceEpoch:          legacy.Identity.Epoch,
		Generation:           legacyGeneration,
		PricingValueRevision: legacy.PricingValueRevision,
		ArchiveGeneration:    legacy.ArchiveGeneration,
		ToEventID:            812,
		SourceTargetEventID:  900,
		Events:               []EventRecord{{ID: 812, EventHash: "event-812"}},
		Rows: []MaterializedRow{{
			Key: "account-a", SubjectKey: "account-a", PayloadJSON: `{"calls":1}`,
		}},
	}); err != nil {
		t.Fatalf("seed stale legacy pricing generation: %v", err)
	}

	migrated, err := MigrateLegacyPricingGeneration(ctx, sink, current)
	if err != nil {
		t.Fatalf("reject stale legacy generation: %v", err)
	}
	if migrated {
		t.Fatal("different-value legacy generation was promoted")
	}
	state, err := sink.State(ctx, StreamPricing)
	if err != nil {
		t.Fatalf("read retained legacy state: %v", err)
	}
	if state.Generation != legacyGeneration || state.CoverageEventID != 812 ||
		state.PricingValueRevision != "value-old" {
		t.Fatalf("stale legacy state was changed: %#v", state)
	}
	rows, err := sink.Rows(ctx, StreamPricing, legacyGeneration)
	if err != nil {
		t.Fatalf("read retained legacy rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stale legacy rows were changed: %#v", rows)
	}
}
