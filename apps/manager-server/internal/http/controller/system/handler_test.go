package system

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestDerivedStatusIncludesPricingRevisionTransition(t *testing.T) {
	ctx := context.Background()
	primaryPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(primaryPath)
	if err != nil {
		t.Fatalf("open primary database: %v", err)
	}
	st := store.New(db)
	t.Cleanup(func() { _ = st.Close() })
	if _, err := db.Exec(`insert into usage_events(
		event_hash, timestamp_ms, timestamp, provider, model, requested_model,
		resolved_model, auth_index, source, auth_file_snapshot,
		auth_provider_snapshot, input_tokens, output_tokens, total_tokens,
		failed, created_at_ms
	) values ('status-pricing-event', 1, '1970-01-01T00:00:00Z', 'codex', 'gpt-test',
		'gpt-test', 'gpt-test', 'idx-1', 'credential.json', 'credential.json',
		'codex', 1, 0, 1, 0, 1)`); err != nil {
		t.Fatalf("seed source event: %v", err)
	}
	derivedPath := filepath.Join(t.TempDir(), "usage-derived.sqlite")
	cfg := config.Config{
		DBPath:                 primaryPath,
		DerivedDBPath:          derivedPath,
		DerivedStorageMode:     config.DerivedStorageSeparate,
		DerivedSourceBatchSize: 10,
		DerivedCycleMaxBatches: 4,
	}
	if err := st.ConfigureDerivedStorage(ctx, cfg); err != nil {
		t.Fatalf("configure separate derived storage: %v", err)
	}
	if _, err := st.RunSeparateDerivedCycle(ctx); err != nil {
		t.Fatalf("run separate derived cycle: %v", err)
	}
	derivedDB, err := sqliterepo.OpenDerived(derivedPath)
	if err != nil {
		t.Fatalf("open derived database: %v", err)
	}
	if _, err := derivedDB.Exec(`update derived_stream_state set
		pricing_structure_revision = 'structure-v1', pricing_revision = 'value-v2',
		last_transition = 'rate_only_refresh' where stream_name = 'pricing'`); err != nil {
		_ = derivedDB.Close()
		t.Fatalf("seed pricing transition metadata: %v", err)
	}
	if err := derivedDB.Close(); err != nil {
		t.Fatalf("close derived database: %v", err)
	}

	status, err := derivedStatus(ctx, &app.Context{Store: st, Config: cfg})
	if err != nil {
		t.Fatalf("read derived status: %v", err)
	}
	for _, stream := range status.Streams {
		if stream.Name != "pricing" {
			continue
		}
		if stream.PricingStructureRevision != "structure-v1" ||
			stream.PricingValueRevision != "value-v2" ||
			stream.LastTransition != "rate_only_refresh" {
			t.Fatalf("pricing transition metadata = %#v", stream)
		}
		if stream.TargetEventID != 1 {
			t.Fatalf("pricing target = %d, want live primary target 1", stream.TargetEventID)
		}
		return
	}
	t.Fatalf("pricing status missing: %#v", status)
}
