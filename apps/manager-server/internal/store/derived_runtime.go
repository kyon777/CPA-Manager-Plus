package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/derivedstore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type separateDerivedRuntime struct {
	sourceDB      *sql.DB
	derivedDB     *sql.DB
	source        derivedstore.Source
	sink          derivedstore.Sink
	engine        *derivedstore.Engine
	sourceRollups usagerollup.Repository
	sourcePricing usagepricing.Repository
	mu            sync.RWMutex
}

// visibleAccountTailLimit is deliberately finite: a page read may merge a
// short live tail, but it must never turn into a full-history recovery query.
// If the tail exceeds this bound, the verified derived rows remain visible and
// the asynchronous worker continues to own completeness.
const visibleAccountTailLimit = 5000

// ConfigureDerivedStorage opens the independent source/sink pair. It is a
// no-op for primary mode, preserving the legacy single-database path.
func (s *Store) ConfigureDerivedStorage(ctx context.Context, cfg config.Config) error {
	if s == nil {
		return errors.New("store is unavailable")
	}
	s.derivedRuntimeMu.Lock()
	s.derivedStorageMode = cfg.DerivedStorageMode
	s.derivedStorageError = ""
	s.derivedRuntimeMu.Unlock()
	if cfg.DerivedStorageMode == config.DerivedStorageSeparate {
		// A reconfiguration must not leave an older source/sink pair active
		// while the replacement is being validated. The guard below remains
		// fail-closed if opening the replacement fails.
		s.closeSeparateDerivedRuntime()
	}
	if s.UsageArchives != nil {
		if cfg.DerivedStorageMode == config.DerivedStorageSeparate {
			// Install the fail-closed guard before any independent DB open. A
			// damaged/unavailable derived DB must not create a window in which
			// archive cleanup can delete primary facts without proof.
			s.UsageArchives.SetDerivedCoverageGuard(usagearchiveGuard{s: s})
		} else {
			s.UsageArchives.SetDerivedCoverageGuard(nil)
		}
	}
	if cfg.DerivedStorageMode != config.DerivedStorageSeparate {
		s.closeSeparateDerivedRuntime()
		return nil
	}
	if cfg.DBPath == "" || cfg.DerivedDBPath == "" {
		s.markSeparateDerivedDegraded(errors.New("separate derived storage requires primary and derived database paths"))
		return nil
	}
	sourceDB, err := sqliterepo.OpenReadOnly(cfg.DBPath)
	if err != nil {
		s.markSeparateDerivedDegraded(err)
		return nil
	}
	derivedDB, err := sqliterepo.OpenDerived(cfg.DerivedDBPath)
	if err != nil {
		_ = sourceDB.Close()
		s.markSeparateDerivedDegraded(err)
		return nil
	}
	runtime := &separateDerivedRuntime{
		sourceDB:      sourceDB,
		derivedDB:     derivedDB,
		sourceRollups: usagerollup.New(sourceDB),
		sourcePricing: usagepricing.New(sourceDB),
	}
	runtime.source = derivedstore.NewSourceWithArchiveDir(sourceDB, cfg.UsageArchiveDir)
	runtime.sink = derivedstore.NewSink(derivedDB)
	runtime.engine = derivedstore.NewEngine(derivedstore.EngineConfig{
		Source:                   runtime.source,
		Sink:                     runtime.sink,
		BatchSize:                cfg.DerivedSourceBatchSize,
		MaxBatches:               cfg.DerivedCycleMaxBatches,
		Yield:                    cfg.DerivedYield,
		PricingCatchupBatchSize:  cfg.DerivedPricingCatchupBatchSize,
		PricingCatchupMaxBatches: cfg.DerivedPricingCatchupMaxBatches,
		PricingCatchupLagEvents:  int64(cfg.DerivedPricingCatchupLagEvents),
	})
	if err := runtime.bootstrapLegacyDerived(ctx); err != nil {
		_ = runtime.close()
		s.markSeparateDerivedDegraded(fmt.Errorf("bootstrap separate derived storage: %w", err))
		return nil
	}
	s.closeSeparateDerivedRuntime()
	s.derivedRuntimeMu.Lock()
	s.derivedRuntime = runtime
	s.derivedRuntimeMu.Unlock()
	if s.UsageArchives != nil {
		s.UsageArchives.SetDerivedCoverageGuard(usagearchiveGuard{s: s})
	}
	_ = ctx // reserved for future startup validation without a primary write
	return nil
}

// markSeparateDerivedDegraded records a sanitized startup failure without
// falling back to the legacy primary writer path. Raw collection, billing and
// control-plane operations may continue; the separate-derived readers report
// degraded/pending until an operator repairs the independent database and
// restarts CPAM.
func (s *Store) markSeparateDerivedDegraded(err error) {
	if s == nil {
		return
	}
	s.derivedRuntimeMu.Lock()
	defer s.derivedRuntimeMu.Unlock()
	if err == nil {
		s.derivedStorageError = ""
		return
	}
	// Keep the public status code stable and do not expose filesystem paths,
	// credentials, SQL text, or other potentially sensitive diagnostics.
	s.derivedStorageError = "derived_runtime_unavailable"
}

func (s *Store) SeparateDerivedStartupError() string {
	if s == nil {
		return ""
	}
	s.derivedRuntimeMu.RLock()
	defer s.derivedRuntimeMu.RUnlock()
	return s.derivedStorageError
}

type usagearchiveGuard struct{ s *Store }

func (g usagearchiveGuard) ValidateArchiveDelete(ctx context.Context, run usagearchive.Run) error {
	if g.s == nil {
		return errors.New("separate derived storage is unavailable")
	}
	ready, err := g.s.SeparateDerivedArchiveDeleteReady(ctx, run.TargetEventID)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("separate derived streams have not covered the archive target")
	}
	return nil
}

func (s *Store) StartSeparateDerived(ctx context.Context) {
	if runtime := s.separateRuntime(); runtime != nil {
		runtime.engine.Start(ctx)
	}
}

// StopSeparateDerived drains the isolated worker before its source/sink
// connections are closed. It is deliberately a no-op when the mode is not
// configured so primary storage shutdown remains unchanged.
func (s *Store) StopSeparateDerived(ctx context.Context) {
	if runtime := s.separateRuntime(); runtime != nil && runtime.engine != nil {
		_ = runtime.engine.StopAndWait(ctx)
	}
}

func (s *Store) WakeSeparateDerived() {
	if runtime := s.separateRuntime(); runtime != nil {
		runtime.engine.Wake()
	}
}

// deferSeparateDerivedCatchUp is the write-boundary guard for legacy derived
// repositories. Separate mode owns its materialized writes in usage-derived
// SQLite; allowing one old catch-up method through would silently reintroduce
// write contention on usage.sqlite. The independent worker is nudged, but the
// synchronous caller receives Pending instead of opening a primary write tx.
func (s *Store) deferSeparateDerivedCatchUp() bool {
	if !s.SeparateDerivedEnabled() {
		return false
	}
	s.WakeSeparateDerived()
	return true
}

func (s *Store) RunSeparateDerivedCycle(ctx context.Context) (derivedstore.CycleResult, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		return derivedstore.CycleResult{}, errors.New("separate derived storage is not configured")
	}
	return runtime.engine.RunCycle(ctx)
}

func (s *Store) SeparateDerivedEnabled() bool {
	if s == nil {
		return false
	}
	s.derivedRuntimeMu.RLock()
	separate := s.derivedStorageMode == config.DerivedStorageSeparate
	s.derivedRuntimeMu.RUnlock()
	return separate
}

func (s *Store) SeparateDerivedRuntimeAvailable() bool {
	return s.separateRuntime() != nil
}

func (s *Store) SeparateDerivedStatus(ctx context.Context) (derivedstore.Status, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		if !s.SeparateDerivedEnabled() {
			return derivedstore.Status{}, errors.New("separate derived storage is not configured")
		}
		return derivedstore.Status{
			Streams: []derivedstore.StreamState{{
				Stream:        "runtime",
				Status:        "degraded",
				LastErrorCode: "derived_runtime_unavailable",
			}},
		}, nil
	}
	status, err := runtime.sink.Status(ctx)
	if err != nil {
		return derivedstore.Status{}, err
	}
	// The sink's target is the most recent worker snapshot. Refresh it from
	// the query-only source on every status read so a paused worker cannot make
	// a multi-million-event backlog look like lag=0.
	snapshot, err := runtime.source.Snapshot(ctx)
	if err != nil {
		return derivedstore.Status{}, err
	}
	for index := range status.Streams {
		status.Streams[index].TargetEventID = snapshot.TargetEventID
	}
	return status, nil
}

// SeparateDerivedArchiveDeleteReady is the explicit fail-closed handoff used
// by archive cleanup. The independent store must have epoch-consistent,
// complete materializations for every read surface that can outlive raw rows
// (history, pricing, hourly dashboard, and monitoring) through the run's
// immutable target event before any raw event may be deleted from the primary
// database. Primary mode deliberately returns true to preserve its existing
// archive contract.
func (s *Store) SeparateDerivedArchiveDeleteReady(ctx context.Context, targetEventID int64) (bool, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		return !s.SeparateDerivedEnabled(), nil
	}
	snapshot, err := runtime.source.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	if targetEventID < 0 || snapshot.Identity.Epoch == "" {
		return false, nil
	}
	// Every read surface that can outlive raw rows must be materialized in the
	// isolated database before archive cleanup may delete a fact. Account history
	// and pricing are already implemented; the hourly/monitoring streams are
	// intentionally included here now so cleanup fails closed until their own
	// isolated readers are wired. This prevents a fresh separate store from
	// silently losing dashboard or analytics history after a raw archive delete.
	for _, stream := range []derivedstore.Stream{
		derivedstore.StreamAccountHistory,
		derivedstore.StreamPricing,
		derivedstore.StreamDashboardHourly,
		derivedstore.StreamMonitoringProjection,
		derivedstore.StreamMonitoringMetadata,
		derivedstore.StreamMonitoringStats,
	} {
		state, err := runtime.sink.State(ctx, stream)
		if err != nil {
			return false, err
		}
		if !derivedStateReady(state, derivedstore.GenerationFor(stream, snapshot), snapshot, stream, targetEventID) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) separateDerivedCheckpoint(ctx context.Context) (UsageRollupCheckpoint, bool, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		if s.SeparateDerivedEnabled() {
			return UsageRollupCheckpoint{
				Name:      usagerollup.AccountHistoryCheckpointName,
				LastError: "derived_runtime_unavailable",
			}, true, nil
		}
		return UsageRollupCheckpoint{}, false, nil
	}
	state, err := runtime.sink.State(ctx, derivedstore.StreamAccountHistory)
	if err != nil {
		return UsageRollupCheckpoint{}, true, err
	}
	return UsageRollupCheckpoint{
		Name:                usagerollup.AccountHistoryCheckpointName,
		LastEventID:         state.CoverageEventID,
		UpdatedAtMS:         state.LastSuccessAtMS,
		LastError:           state.LastErrorCode,
		LastRunFinishedAtMS: state.LastSuccessAtMS,
	}, true, nil
}

func (s *Store) separateDerivedLatestEventID(ctx context.Context) (int64, bool, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		if s.SeparateDerivedEnabled() {
			return 0, true, nil
		}
		return 0, false, nil
	}
	snapshot, err := runtime.source.Snapshot(ctx)
	if err != nil {
		return 0, true, err
	}
	return snapshot.TargetEventID, true, nil
}

func (s *Store) separateDerivedPricingCatchUp(ctx context.Context) (UsagePricingCatchUpResult, bool, error) {
	runtime := s.separateRuntime()
	if runtime == nil {
		if s.SeparateDerivedEnabled() {
			return UsagePricingCatchUpResult{Pending: true}, true, nil
		}
		return UsagePricingCatchUpResult{}, false, nil
	}
	runtime.engine.Wake()
	state, err := runtime.sink.State(ctx, derivedstore.StreamPricing)
	if err != nil {
		return UsagePricingCatchUpResult{}, true, err
	}
	snapshot, err := runtime.source.Snapshot(ctx)
	if err != nil {
		return UsagePricingCatchUpResult{}, true, err
	}
	return UsagePricingCatchUpResult{
		LastEventID:     state.CoverageEventID,
		CoverageEventID: state.CoverageEventID,
		TargetEventID:   snapshot.TargetEventID,
		Pending:         state.CoverageEventID < snapshot.TargetEventID || state.Status != "ready",
	}, true, nil
}

func (s *Store) separateRuntime() *separateDerivedRuntime {
	if s == nil {
		return nil
	}
	s.derivedRuntimeMu.RLock()
	runtime := s.derivedRuntime
	s.derivedRuntimeMu.RUnlock()
	return runtime
}

func (s *Store) closeSeparateDerivedRuntime() {
	if s == nil {
		return
	}
	s.derivedRuntimeMu.Lock()
	runtime := s.derivedRuntime
	s.derivedRuntime = nil
	s.derivedRuntimeMu.Unlock()
	if s.UsageArchives != nil {
		s.UsageArchives.SetDerivedCoverageGuard(nil)
	}
	if runtime != nil {
		_ = runtime.close()
	}
}

func (r *separateDerivedRuntime) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if r.engine != nil {
		if err := r.engine.StopAndWait(stopCtx); err != nil {
			first = err
		}
	}
	cancel()
	if r.sourceDB != nil {
		if err := r.sourceDB.Close(); err != nil && first == nil {
			first = err
		}
		r.sourceDB = nil
	}
	if r.derivedDB != nil {
		if err := r.derivedDB.Close(); err != nil && first == nil {
			first = err
		}
		r.derivedDB = nil
	}
	return first
}

// loadLegacySnapshot is a display-only warm-up fallback. Both repository
// handles use the query-only source connection, so this path can preserve the
// last known account totals without opening a read transaction on Store.db
// (whose DSN is also used by primary writers). It never reports fresh
// coverage; the isolated worker remains the authority for readiness.
func (r *separateDerivedRuntime) loadLegacySnapshot(ctx context.Context, accountKeys []string, prices map[string]ModelPrice) (UsagePricingAccountSnapshot, error) {
	if r == nil || r.sourceDB == nil || r.sourceRollups == nil || r.sourcePricing == nil {
		return UsagePricingAccountSnapshot{}, errors.New("separate derived source fallback is unavailable")
	}
	tx, err := r.sourceDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	coreRows, err := r.sourceRollups.AccountHistoryRowsTx(ctx, tx, accountKeys)
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	rows, state, incomplete, err := loadVerifiedAccountPricingRowsTx(ctx, tx, r.sourcePricing, coreRows, accountKeys)
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	return UsagePricingAccountSnapshot{
		CoreRows:                     coreRows,
		Rows:                         rows,
		State:                        state,
		Available:                    false,
		PricingIncompleteAccountKeys: incomplete,
		Prices:                       prices,
	}, nil
}

// loadExactCurrentPagePricing reads only the requested page's account keys
// through the runtime query-only source handle. It deliberately shares the
// same coverage verifier as the primary reader: a derived lag may delay
// materialization, but it must not turn a verifiable current-page amount into
// a blank value or trigger a global event scan.
func (r *separateDerivedRuntime) loadExactCurrentPagePricing(
	ctx context.Context,
	accountKeys []string,
	coreRows []AccountHistoryRollupRow,
) ([]UsagePricingAccountRow, map[string]struct{}, error) {
	if r == nil || r.sourceDB == nil || r.sourcePricing == nil {
		return nil, nil, errors.New("separate derived source pricing fallback is unavailable")
	}
	tx, err := r.sourceDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, _, incomplete, err := loadVerifiedAccountPricingRowsTx(ctx, tx, r.sourcePricing, coreRows, accountKeys)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return rows, incomplete, nil
}

func (r *separateDerivedRuntime) loadAccountSnapshot(ctx context.Context, keys []string, prices map[string]ModelPrice) (UsagePricingAccountSnapshot, bool, error) {
	if r == nil || r.source == nil || r.sink == nil {
		return UsagePricingAccountSnapshot{}, false, nil
	}
	snapshot, err := r.source.Snapshot(ctx)
	if err != nil {
		return UsagePricingAccountSnapshot{}, false, err
	}
	coreState, err := r.sink.State(ctx, derivedstore.StreamAccountHistory)
	if err != nil {
		return UsagePricingAccountSnapshot{}, false, err
	}
	pricingState, err := r.sink.State(ctx, derivedstore.StreamPricing)
	if err != nil {
		return UsagePricingAccountSnapshot{}, false, err
	}
	// A rebuild is intentionally asynchronous. If a previous generation has
	// already verified rows for the requested page, expose those rows while the
	// source tail is being applied instead of replacing the page with an empty
	// "syncing" shell. The Available bit remains false until both streams cover
	// the same source watermark, so callers can label the values as stale.
	var coreRows, pricingRows []derivedstore.MaterializedRow
	if strings.TrimSpace(coreState.Generation) != "" {
		coreRows, err = r.sink.RowsForSubjects(ctx, derivedstore.StreamAccountHistory, coreState.Generation, keys)
		if err != nil {
			return UsagePricingAccountSnapshot{}, false, err
		}
	}
	if strings.TrimSpace(pricingState.Generation) != "" {
		pricingRows, err = r.sink.RowsForSubjects(ctx, derivedstore.StreamPricing, pricingState.Generation, keys)
		if err != nil {
			return UsagePricingAccountSnapshot{}, false, err
		}
	}
	mergeTail := func(stream derivedstore.Stream, state derivedstore.StreamState, rows []derivedstore.MaterializedRow) ([]derivedstore.MaterializedRow, error) {
		if len(keys) == 0 || snapshot.TargetEventID <= state.CoverageEventID {
			return rows, nil
		}
		events, err := r.source.EventsForSubjectsAfter(ctx, keys, state.CoverageEventID, snapshot.TargetEventID, visibleAccountTailLimit+1)
		if err != nil {
			return nil, err
		}
		// A limit+1 result proves that this page cannot be made exact without
		// waiting for the worker. Do not expose a partial sum as if it were
		// authoritative; keep the last verified rows instead.
		if len(events) > visibleAccountTailLimit {
			return rows, nil
		}
		return derivedstore.MergeMaterializedRows(stream, state.Generation, rows, events, prices, time.Now().UnixMilli())
	}
	coreRows, err = mergeTail(derivedstore.StreamAccountHistory, coreState, coreRows)
	if err != nil {
		return UsagePricingAccountSnapshot{}, false, err
	}
	pricingRows, err = mergeTail(derivedstore.StreamPricing, pricingState, pricingRows)
	if err != nil {
		return UsagePricingAccountSnapshot{}, false, err
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	core := make([]AccountHistoryRollupRow, 0)
	for _, row := range coreRows {
		var aggregate derivedstore.AccountAggregate
		if err := json.Unmarshal([]byte(row.PayloadJSON), &aggregate); err != nil {
			return UsagePricingAccountSnapshot{}, false, fmt.Errorf("decode derived account history row: %w", err)
		}
		if len(keySet) > 0 {
			if _, ok := keySet[aggregate.AccountKey]; !ok {
				continue
			}
		}
		core = append(core, toAccountHistoryRow(aggregate))
	}
	pricing := make([]UsagePricingAccountRow, 0)
	for _, row := range pricingRows {
		var aggregate derivedstore.AccountAggregate
		if err := json.Unmarshal([]byte(row.PayloadJSON), &aggregate); err != nil {
			return UsagePricingAccountSnapshot{}, false, fmt.Errorf("decode derived pricing row: %w", err)
		}
		if len(keySet) > 0 {
			if _, ok := keySet[aggregate.AccountKey]; !ok {
				continue
			}
		}
		pricing = append(pricing, toPricingAccountRow(aggregate))
	}
	historyReady := derivedStateReady(coreState, derivedstore.GenerationFor(derivedstore.StreamAccountHistory, snapshot), snapshot, derivedstore.StreamAccountHistory, snapshot.TargetEventID)
	pricingReady := derivedStateReady(pricingState, derivedstore.GenerationFor(derivedstore.StreamPricing, snapshot), snapshot, derivedstore.StreamPricing, snapshot.TargetEventID)
	incomplete := accountPricingCoverageIncompleteKeys(core, pricing)
	// Global pricing lag is a readiness signal, not evidence that this bounded
	// page is incomplete. mergeTail has already incorporated every visible
	// account event in the short live tail above. Only fall back to the primary
	// pricing reader when the page-level coverage check proves a missing row;
	// otherwise an unrelated event can turn a fast page request into a large
	// primary pricing-tail scan.
	if len(incomplete) > 0 {
		exactRows, exactIncomplete, exactErr := r.loadExactCurrentPagePricing(ctx, keys, core)
		if exactErr == nil {
			pricing = exactRows
			incomplete = exactIncomplete
		} else {
			// The page remains usable if the optional exact source read is
			// temporarily unavailable. Mark all core accounts unpriced so
			// monitoring safely retains request/token totals without presenting a
			// stale amount as current.
			incomplete = make(map[string]struct{}, len(core))
			for _, row := range core {
				incomplete[row.AccountKey] = struct{}{}
			}
		}
	}
	ready := historyReady && pricingReady
	return UsagePricingAccountSnapshot{
		CoreRows:                     core,
		Rows:                         pricing,
		State:                        toUsagePricingState(pricingState, snapshot.PricingRevision),
		Available:                    ready,
		PricingIncompleteAccountKeys: incomplete,
		Prices:                       prices,
	}, ready, nil
}

func derivedStateReady(state derivedstore.StreamState, expectedGeneration string, snapshot derivedstore.SourceSnapshot, stream derivedstore.Stream, target int64) bool {
	if state.Status != "ready" || state.CoverageEventID < target || strings.TrimSpace(expectedGeneration) == "" {
		return false
	}
	if state.SourceEpoch != snapshot.Identity.Epoch || state.Generation != expectedGeneration {
		return false
	}
	if state.ArchiveGeneration != snapshot.ArchiveGeneration {
		return false
	}
	if stream == derivedstore.StreamPricing && state.PricingRevision != snapshot.PricingRevision {
		return false
	}
	return true
}

func toAccountHistoryRow(a derivedstore.AccountAggregate) AccountHistoryRollupRow {
	return AccountHistoryRollupRow{
		AccountKey: a.AccountKey, AccountSnapshot: a.AccountSnapshot,
		AuthLabelSnapshot: a.AuthLabelSnapshot, AuthProviderSnapshot: a.AuthProviderSnapshot,
		AuthIndex: a.AuthIndex, Source: a.Source, SourceHash: a.SourceHash,
		Model: a.Model, BillingModel: a.BillingModel, ServiceTier: a.ServiceTier,
		Calls: a.Calls, SuccessCalls: a.SuccessCalls, FailureCalls: a.FailureCalls,
		InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, ReasoningTokens: a.ReasoningTokens,
		CachedTokens: a.CachedTokens, CacheReadTokens: a.CacheReadTokens, CacheCreationTokens: a.CacheCreationTokens,
		LongContextTokens: usageLongContext(a), TotalTokens: a.TotalTokens,
		FirstSeenMS: a.FirstSeenMS, LastSeenMS: a.LastSeenMS, UpdatedAtMS: a.UpdatedAtMS,
	}
}

func toPricingAccountRow(a derivedstore.AccountAggregate) UsagePricingAccountRow {
	row := UsagePricingAccountRow{
		AccountKey: a.AccountKey, AccountSnapshot: a.AccountSnapshot,
		AuthLabelSnapshot: a.AuthLabelSnapshot, AuthProviderSnapshot: a.AuthProviderSnapshot,
		AuthIndex: a.AuthIndex, Source: a.Source, SourceHash: a.SourceHash,
		Model: a.Model, BillingModel: a.BillingModel,
		ServiceTier: a.ServiceTier,
		Calls:       a.Calls, SuccessCalls: a.SuccessCalls, FailureCalls: a.FailureCalls,
		InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, ReasoningTokens: a.ReasoningTokens,
		CachedTokens: a.CachedTokens, CacheReadTokens: a.CacheReadTokens, CacheCreationTokens: a.CacheCreationTokens,
		LongContextTokens: usageLongContext(a), TotalTokens: a.TotalTokens,
		FirstSeenMS: a.FirstSeenMS, LastSeenMS: a.LastSeenMS, UpdatedAtMS: a.UpdatedAtMS,
	}
	row.PricingModel = a.PricingModel
	row.ContextThresholdTokens = a.ContextThresholdTokens
	return row
}

func usageLongContext(a derivedstore.AccountAggregate) usage.LongContextTokens {
	return usage.LongContextTokens{
		LongInputTokens: a.LongInputTokens, LongOutputTokens: a.LongOutputTokens,
		LongCachedTokens: a.LongCachedTokens, LongCacheReadTokens: a.LongCacheReadTokens,
		LongCacheCreationTokens: a.LongCacheCreationTokens,
	}
}

func toUsagePricingState(state derivedstore.StreamState, revision string) UsagePricingState {
	return UsagePricingState{
		RollupName: usagepricing.RollupName, SchemaVersion: usagepricing.SchemaVersion,
		StructureRevision: revision, Status: state.Status,
		CoverageEventID: state.CoverageEventID, TargetEventID: state.TargetEventID,
		ProcessedEvents: state.ProcessedEvents, UpdatedAtMS: state.LastSuccessAtMS,
	}
}
