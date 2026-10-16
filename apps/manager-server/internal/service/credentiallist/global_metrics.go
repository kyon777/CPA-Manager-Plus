package credentiallist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	globalMetricsWarming     = "warming"
	globalMetricsReady       = "ready"
	globalMetricsStale       = "stale"
	globalMetricsUnavailable = "unavailable"
	globalMetricsTTL         = 15 * time.Second
	globalMetricsTimeout     = 10 * time.Second
	globalEvidenceLookback   = 45 * 24 * time.Hour
	globalEvidenceLimit      = 10000
)

// GlobalMetrics is the small, full-inventory projection used by the dashboard
// cards. It deliberately contains counts only; credential JSON and usage
// details stay on their existing paths.
type GlobalMetrics struct {
	Total                 int `json:"total"`
	Enabled               int `json:"enabled"`
	Available             int `json:"available"`
	NeedsAttention        int `json:"needs_attention"`
	QuotaRisk             int `json:"quota_risk"`
	Disabled              int `json:"disabled"`
	Unconfirmed           int `json:"unconfirmed"`
	NeedsInspectionAction int `json:"needs_inspection_action"`
}

// GlobalCredentialEvidence is an internal, non-sensitive health projection.
// A credential can carry several evidence flags; ComputeGlobalMetrics applies
// the same mutually-exclusive precedence used by the browser cards.
type GlobalCredentialEvidence struct {
	HasEvidence           bool
	Available             bool
	NeedsAttention        bool
	QuotaRisk             bool
	NeedsInspectionAction bool
}

// GlobalMetricsReader supplies durable evidence for a complete inventory
// snapshot. Implementations must be read-only and must not call CPA model
// endpoints or mutate usage/billing state.
type GlobalMetricsReader interface {
	ReadGlobalMetrics(context.Context, []map[string]any) (GlobalMetrics, error)
}

// ComputeGlobalMetrics partitions the complete inventory. Disabled is always
// counted first; for enabled credentials the operational precedence is
// attention > quota risk > available > unconfirmed.
func ComputeGlobalMetrics(items []map[string]any, evidence map[string]GlobalCredentialEvidence) GlobalMetrics {
	metrics := GlobalMetrics{Total: len(items)}
	for _, item := range items {
		key := itemKey(item)
		ev := evidence[key]
		if readBool(item, "disabled") {
			metrics.Disabled++
		} else {
			metrics.Enabled++
			switch {
			case ev.NeedsAttention:
				metrics.NeedsAttention++
			case ev.QuotaRisk:
				metrics.QuotaRisk++
			case ev.Available:
				metrics.Available++
			default:
				metrics.Unconfirmed++
			}
		}
		if ev.NeedsInspectionAction {
			metrics.NeedsInspectionAction++
		}
	}
	return metrics
}

// NewWithGlobalMetricsReader is a small constructor used by isolated service
// tests and integrations that only need the inventory and global projection.
func NewWithGlobalMetricsReader(reader InventoryReader, metrics GlobalMetricsReader, clock ...func() time.Time) *Service {
	return newService(reader, nil, nil, metrics, clock...)
}

// NewWithRecentRecoveryAndGlobalMetricsReader preserves the existing recent
// sort and recovery enrichment while adding the full-inventory projection.
func NewWithRecentRecoveryAndGlobalMetricsReader(
	reader InventoryReader,
	recent RecentReader,
	recovery RecoveryReader,
	metrics GlobalMetricsReader,
	clock ...func() time.Time,
) *Service {
	return newService(reader, recent, recovery, metrics, clock...)
}

// storeGlobalMetricsReader reads only Manager's indexed projections. It is
// intentionally kept behind GlobalMetricsReader so the list service remains
// testable without opening SQLite.
type storeGlobalMetricsReader struct {
	store  *store.Store
	recent RecentReader
	now    func() time.Time
}

func NewStoreGlobalMetricsReader(st *store.Store, recent RecentReader, clock ...func() time.Time) GlobalMetricsReader {
	if st == nil {
		return nil
	}
	now := time.Now
	if len(clock) > 0 && clock[0] != nil {
		now = clock[0]
	}
	return &storeGlobalMetricsReader{store: st, recent: recent, now: now}
}

// resolveGlobalMetrics never blocks a page response on the first full-store
// evidence scan. The scan is coalesced and runs in the background; the next
// normal page poll observes the ready projection.
func (s *Service) resolveGlobalMetrics(ctx context.Context, cacheKey string, current *snapshot) (*GlobalMetrics, string, int64) {
	if s == nil || s.globalMetrics == nil {
		return nil, globalMetricsUnavailable, 0
	}
	if current == nil || !current.complete || current.syncError != "" {
		return nil, globalMetricsWarming, 0
	}
	fingerprint := globalMetricsFingerprint(current.items)
	now := s.now()
	s.mu.Lock()
	if s.globalMetricsCache == nil {
		s.globalMetricsCache = make(map[string]*globalMetricsEntry)
	}
	entry := s.globalMetricsCache[cacheKey]
	var staleMetrics *GlobalMetrics
	var staleGeneratedAt int64
	if entry != nil && entry.ready && entry.err == nil && entry.fingerprint != fingerprint {
		copy := entry.metrics
		staleMetrics = &copy
		staleGeneratedAt = entry.generatedAt.UnixMilli()
	}
	if entry != nil && entry.fingerprint == fingerprint && entry.ready {
		if entry.err == nil && now.Sub(entry.generatedAt) < globalMetricsTTL {
			metrics := entry.metrics
			generatedAt := entry.generatedAt.UnixMilli()
			s.mu.Unlock()
			return &metrics, globalMetricsReady, generatedAt
		}
		if entry.err != nil && now.Sub(entry.generatedAt) < time.Second {
			if entry.stale != nil {
				metrics := *entry.stale
				generatedAt := entry.staleAt.UnixMilli()
				s.mu.Unlock()
				return &metrics, globalMetricsStale, generatedAt
			}
			s.mu.Unlock()
			return nil, globalMetricsUnavailable, 0
		}
	}
	if entry != nil && entry.fingerprint == fingerprint && entry.loading {
		if entry.stale != nil {
			metrics := *entry.stale
			generatedAt := entry.staleAt.UnixMilli()
			s.mu.Unlock()
			return &metrics, globalMetricsStale, generatedAt
		}
		s.mu.Unlock()
		return nil, globalMetricsWarming, 0
	}
	previous := entry
	entry = &globalMetricsEntry{
		fingerprint: fingerprint,
		loading:     true,
		done:        make(chan struct{}),
	}
	if previous != nil && previous.ready && previous.err == nil {
		stale := previous.metrics
		entry.stale = &stale
		entry.staleAt = previous.generatedAt
		if staleMetrics == nil {
			staleCopy := stale
			staleMetrics = &staleCopy
			staleGeneratedAt = previous.generatedAt.UnixMilli()
		}
	}
	s.globalMetricsCache[cacheKey] = entry
	items := cloneItems(current.items)
	s.mu.Unlock()

	// The request context may be tied to a browser navigation. The background
	// computation must finish for other tabs even if that navigation is closed.
	go func() {
		backgroundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), globalMetricsTimeout)
		defer cancel()
		metrics, err := s.globalMetrics.ReadGlobalMetrics(backgroundCtx, items)
		s.mu.Lock()
		if currentEntry := s.globalMetricsCache[cacheKey]; currentEntry == entry {
			entry.metrics = metrics
			entry.err = err
			entry.generatedAt = s.now()
			entry.loading = false
			entry.ready = true
			entry.stale = nil
			entry.staleAt = time.Time{}
			close(entry.done)
		}
		s.mu.Unlock()
	}()
	if staleMetrics != nil {
		return staleMetrics, globalMetricsStale, staleGeneratedAt
	}
	return nil, globalMetricsWarming, 0
}

func (r *storeGlobalMetricsReader) ReadGlobalMetrics(ctx context.Context, items []map[string]any) (GlobalMetrics, error) {
	if r == nil || r.store == nil {
		return GlobalMetrics{}, errors.New("global metrics store is unavailable")
	}
	evidence := make(map[string]GlobalCredentialEvidence, len(items))
	aliases := buildGlobalAliasIndex(items)
	mark := func(identity globalEvidenceIdentity, apply func(*GlobalCredentialEvidence)) {
		for _, index := range aliases.match(identity) {
			key := itemKey(items[index])
			ev := evidence[key]
			ev.HasEvidence = true
			apply(&ev)
			evidence[key] = ev
		}
	}

	if r.recent != nil {
		targets := make([]store.LatestAccountRequestQuery, 0, len(items))
		for index, item := range items {
			targets = append(targets, store.LatestAccountRequestQuery{
				RequestIndex:          index,
				AuthFileSnapshot:      readString(item, "name", "file_name", "fileName"),
				AuthIndex:             readString(item, "auth_index", "authIndex"),
				Provider:              readString(item, "provider", "type"),
				AuthAccountIDSnapshot: readString(item, "chatgpt_account_id", "account_id", "accountId"),
				AuthProjectIDSnapshot: readString(item, "project_id", "projectId"),
				AccountSnapshot:       readString(item, "account_snapshot", "accountSnapshot", "email", "account"),
			})
		}
		requests, err := r.recent.RecentAccountRequests(ctx, targets, 1)
		if err != nil {
			return GlobalMetrics{}, err
		}
		for _, request := range requests {
			if request.RequestIndex < 0 || request.RequestIndex >= len(items) {
				continue
			}
			item := items[request.RequestIndex]
			identity := identityFromItem(item)
			if request.Failed {
				mark(identity, func(ev *GlobalCredentialEvidence) { ev.NeedsAttention = true })
			} else {
				mark(identity, func(ev *GlobalCredentialEvidence) { ev.Available = true })
			}
		}
	}

	nowMS := r.now().UnixMilli()
	if r.store.QuotaCooldowns != nil {
		cooldowns, err := r.store.QuotaCooldowns.ListActive(ctx)
		if err != nil {
			return GlobalMetrics{}, err
		}
		for _, cooldown := range cooldowns {
			if cooldown.RecoverAtMS > 0 && cooldown.RecoverAtMS <= nowMS {
				continue
			}
			mark(globalEvidenceIdentity{
				file:     cooldown.AuthFileName,
				index:    cooldown.AuthIndex,
				account:  cooldown.AccountSnapshot,
				provider: cooldown.Provider,
			}, func(ev *GlobalCredentialEvidence) { ev.QuotaRisk = true })
		}
	}

	if r.store.AccountActions != nil {
		actions, err := r.store.AccountActions.List(ctx, model.AccountActionStatusPending, globalEvidenceLimit)
		if err != nil {
			return GlobalMetrics{}, err
		}
		for _, action := range actions {
			mark(globalEvidenceIdentity{
				file:      action.AuthFileName,
				index:     action.AuthIndex,
				account:   action.AccountSnapshot,
				accountID: action.AccountIDSnapshot,
				provider:  action.Provider,
			}, func(ev *GlobalCredentialEvidence) {
				ev.NeedsAttention = true
				ev.NeedsInspectionAction = true
			})
		}
	}

	if r.store.CodexInspections != nil {
		if err := r.applyLatestInspectionEvidence(ctx, mark); err != nil {
			return GlobalMetrics{}, err
		}
	}

	// Header snapshots are a quota-only projection and therefore safe to read
	// without downloading credential JSON. A bounded lookback avoids turning a
	// dashboard refresh into an unbounded analytics query.
	if r.store.UsageEvents != nil {
		headers, err := r.store.LatestHeaderSnapshots(ctx, nowMS-globalEvidenceLookback.Milliseconds(), globalEvidenceLimit)
		if err != nil {
			return GlobalMetrics{}, err
		}
		for _, header := range headers {
			metadata := header.ResponseMetadata
			quotaRisk, available, attention := classifyHeaderEvidence(header, nowMS)
			if !quotaRisk && !available && !attention && metadata == nil {
				continue
			}
			mark(globalEvidenceIdentity{
				file:      header.AuthFileSnapshot,
				index:     header.AuthIndex,
				account:   header.AccountSnapshot,
				accountID: header.AuthAccountIDSnapshot,
				provider:  header.AuthProviderSnapshot,
			}, func(ev *GlobalCredentialEvidence) {
				ev.QuotaRisk = ev.QuotaRisk || quotaRisk
				ev.Available = ev.Available || available
				ev.NeedsAttention = ev.NeedsAttention || attention
			})
		}
	}

	return ComputeGlobalMetrics(items, evidence), nil
}

func (r *storeGlobalMetricsReader) applyLatestInspectionEvidence(
	ctx context.Context,
	mark func(globalEvidenceIdentity, func(*GlobalCredentialEvidence)),
) error {
	runs, err := r.store.CodexInspections.ListRuns(ctx, 20)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if model.IsCodexInspectionRunActive(run.Status) {
			continue
		}
		results, err := r.store.CodexInspections.ListResults(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, result := range results {
			statusCode := 0
			if result.StatusCode != nil {
				statusCode = *result.StatusCode
			}
			action := strings.ToLower(strings.TrimSpace(result.Action))
			resultStatus := strings.ToLower(strings.TrimSpace(result.Status))
			resultState := strings.ToLower(strings.TrimSpace(result.State))
			actionStatus := strings.ToLower(strings.TrimSpace(result.ActionStatus))
			attention := action == "reauth" ||
				action == "delete" ||
				action == "disable" ||
				resultStatus == "failed" ||
				resultState == "failed" ||
				actionStatus == model.CodexInspectionActionStatusFailed ||
				actionStatus == model.CodexInspectionActionStatusNeedsReview ||
				statusCode == httpUnauthorized || statusCode == httpForbidden || statusCode == httpBadRequest
			quotaRisk := result.IsQuota && result.UsedPercent != nil && *result.UsedPercent >= quotaRiskUsedPercent
			available := !attention && !quotaRisk && (resultStatus == "success" || resultState == "success" || action == "keep")
			mark(globalEvidenceIdentity{
				file:      result.FileName,
				index:     result.AuthIndex,
				account:   result.AccountSnapshot,
				accountID: result.AccountID,
				provider:  result.Provider,
			}, func(ev *GlobalCredentialEvidence) {
				ev.NeedsAttention = ev.NeedsAttention || attention
				ev.QuotaRisk = ev.QuotaRisk || quotaRisk
				ev.Available = ev.Available || available
				if action != "" && action != "keep" {
					ev.NeedsInspectionAction = true
				}
			})
		}
		// The newest completed run is authoritative for the inspection evidence.
		return nil
	}
	return nil
}

const (
	httpBadRequest       = 400
	httpUnauthorized     = 401
	httpForbidden        = 403
	quotaRiskUsedPercent = 80.0
)

func classifyHeaderEvidence(header store.HeaderSnapshot, nowMS int64) (quotaRisk, available, attention bool) {
	if header.HeaderErrorKind != "" || header.HeaderErrorCode != "" {
		attention = true
	}
	positiveCredits := false
	fiveHourWindow := false
	if header.ResponseMetadata != nil && header.ResponseMetadata.Quota != nil {
		quota := header.ResponseMetadata.Quota
		positiveCredits = quota.CreditsHasCredits != nil && *quota.CreditsHasCredits
		if !positiveCredits {
			positiveCredits = positiveNumericString(quota.CreditsBalance)
		}
		windowKind := strings.ToLower(strings.TrimSpace(quota.ReachedWindowKind))
		if windowKind == "" {
			windowKind = strings.ToLower(strings.TrimSpace(quota.SummaryWindowKind))
		}
		fiveHourWindow = isFiveHourQuotaWindow(windowKind)
	}
	// Positive Credits can keep a weekly/monthly account usable, but an
	// exhausted five-hour window remains a hard availability boundary.
	if header.HeaderQuotaUsedPercent.Valid {
		used := header.HeaderQuotaUsedPercent.Float64
		if used >= quotaRiskUsedPercent && (!positiveCredits || fiveHourWindow) {
			quotaRisk = true
		} else if used < quotaRiskUsedPercent || (positiveCredits && !fiveHourWindow) {
			available = true
		}
	}
	if header.HeaderQuotaRecoverAtMS.Valid && header.HeaderQuotaRecoverAtMS.Int64 > nowMS && (!positiveCredits || fiveHourWindow) {
		quotaRisk = true
	} else if positiveCredits && !fiveHourWindow {
		available = true
	}
	return quotaRisk, available, attention
}

func isFiveHourQuotaWindow(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "", "-", "", " ", "").Replace(value)
	return strings.Contains(value, "fivehour") || strings.Contains(value, "5h") || strings.Contains(value, "5hour")
}

func positiveNumericString(value string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && parsed > 0
}

type globalEvidenceIdentity struct {
	file      string
	index     string
	account   string
	accountID string
	provider  string
}

type globalAliasIndex struct {
	byAlias map[string][]int
	byFile  map[string][]int
}

func buildGlobalAliasIndex(items []map[string]any) globalAliasIndex {
	index := globalAliasIndex{byAlias: make(map[string][]int), byFile: make(map[string][]int)}
	for itemIndex, item := range items {
		identity := identityFromItem(item)
		file := normalizeIdentity(identity.file)
		if file != "" {
			index.byFile[file] = append(index.byFile[file], itemIndex)
		}
		for _, alias := range identity.aliases(false) {
			index.byAlias[alias] = append(index.byAlias[alias], itemIndex)
		}
	}
	return index
}

func (index globalAliasIndex) match(identity globalEvidenceIdentity) []int {
	for _, alias := range identity.aliases(false) {
		if indexes := index.byAlias[alias]; len(indexes) == 1 {
			return indexes
		}
	}
	if file := normalizeIdentity(identity.file); file != "" {
		if indexes := index.byFile[file]; len(indexes) == 1 {
			return indexes
		}
	}
	return nil
}

func identityFromItem(item map[string]any) globalEvidenceIdentity {
	return globalEvidenceIdentity{
		file:      readString(item, "name", "file_name", "fileName"),
		index:     readString(item, "auth_index", "authIndex"),
		account:   readString(item, "account_snapshot", "accountSnapshot", "email", "account"),
		accountID: readString(item, "chatgpt_account_id", "account_id", "accountId"),
		provider:  readString(item, "provider", "type"),
	}
}

func (identity globalEvidenceIdentity) aliases(includeFileOnly bool) []string {
	file := normalizeIdentity(identity.file)
	index := normalizeIdentity(identity.index)
	account := normalizeIdentity(identity.account)
	accountID := normalizeIdentity(identity.accountID)
	provider := normalizeIdentity(identity.provider)
	aliases := make([]string, 0, 5)
	if file != "" && index != "" {
		aliases = append(aliases, "fi\x00"+file+"\x00"+index+"\x00"+provider, "fi\x00"+file+"\x00"+index)
	}
	if file != "" && account != "" {
		aliases = append(aliases, "fa\x00"+file+"\x00"+account)
	}
	if file != "" && accountID != "" {
		aliases = append(aliases, "fd\x00"+file+"\x00"+accountID)
	}
	if account != "" {
		aliases = append(aliases, "a\x00"+account+"\x00"+provider, "a\x00"+account)
	}
	if accountID != "" {
		aliases = append(aliases, "d\x00"+accountID+"\x00"+provider, "d\x00"+accountID)
	}
	if includeFileOnly && file != "" {
		aliases = append(aliases, "f\x00"+file)
	}
	return aliases
}

func normalizeIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func globalMetricsFingerprint(items []map[string]any) string {
	payload, _ := json.Marshal(items)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
