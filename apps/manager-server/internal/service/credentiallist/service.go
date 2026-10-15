package credentiallist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultPageSize = 50
	maxPageSize     = 50
	maxWarmPages    = 2000
	snapshotTTL     = 30 * time.Second
	recentIndexTTL  = 5 * time.Second
	recentErrorTTL  = 1 * time.Second
)

// InventoryReader is deliberately limited to the CPA management inventory
// endpoint. It cannot issue model requests or touch usage tables.
type InventoryReader interface {
	ReadAuthFileInventory(context.Context, url.Values) ([]byte, error)
}

// InventoryScopeReader lets the list cache follow CPA setup changes without
// ever using the management key itself as an in-memory cache key. Implementers
// should return a stable, non-secret digest for the active CPA setup.
type InventoryScopeReader interface {
	AuthFileInventoryScope(context.Context) (string, error)
}

// RecentReader is the indexed, read-only request projection used to preserve
// the existing "latest request" ordering without loading usage history or
// billing aggregates into the credential list.
type RecentReader interface {
	RecentAccountRequests(context.Context, []store.LatestAccountRequestQuery, int) ([]store.LatestAccountRequest, error)
}

type Request struct {
	Page            int
	PageSize        int
	Search          string
	Provider        string
	Status          string
	Sort            string
	IncludeProxyURL bool
}

type Response struct {
	Files            []map[string]any `json:"files"`
	Total            int              `json:"total"`
	InventoryTotal   int              `json:"inventory_total,omitempty"`
	Page             int              `json:"page"`
	PageSize         int              `json:"page_size"`
	HasMore          bool             `json:"has_more"`
	ProxyURLIncluded bool             `json:"proxy_url_included"`
	SyncStatus       string           `json:"sync_status"`
	SyncAgeMS        int64            `json:"sync_age_ms,omitempty"`
	SyncError        string           `json:"sync_error,omitempty"`
	// ProxyURLs is the complete enabled-credential projection when the
	// snapshot has finished warming. It contains no credential tokens.
	ProxyURLs []string `json:"proxy_urls,omitempty"`
	// Keep zero explicit: an empty enabled set is different from an omitted
	// projection while the browser decides whether proxy filtering is complete.
	EnabledCredentialCount  int            `json:"enabled_credential_count"`
	ProxyProjectionComplete bool           `json:"proxy_projection_complete"`
	ProviderCounts          map[string]int `json:"provider_counts,omitempty"`
}

type Service struct {
	reader InventoryReader
	recent RecentReader
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]*snapshot
	loads  map[string]*snapshotLoad
	// recentCache is keyed by the same setup/query variant as cache. The
	// fingerprint inside each entry prevents a partially warmed or mutated
	// snapshot from reusing an older recency projection.
	recentCache map[string]*recentIndexEntry
}

// snapshotLoad coalesces the first page fetch for a cache key. A busy panel
// can issue several identical requests during React mount/retry; only one of
// those requests should reach CPA Core.
type snapshotLoad struct {
	done     chan struct{}
	snapshot *snapshot
	err      error
}

type snapshot struct {
	items            []map[string]any
	totalHint        int
	proxyURLIncluded bool
	fetchedAt        time.Time
	updatedAt        time.Time
	warming          bool
	complete         bool
	syncError        string
}

// recentIndexEntry coalesces the expensive, indexed latest-request lookup for
// one immutable inventory snapshot. Without this small cache every page click
// would issue one or more SQLite lookups for all credentials, even though the
// underlying auth-file snapshot is unchanged.
type recentIndexEntry struct {
	fingerprint string
	values      map[string]int64
	updatedAt   time.Time
	err         error
	loading     bool
	ready       bool
	done        chan struct{}
}

func New(reader InventoryReader, clock ...func() time.Time) *Service {
	return NewWithRecentReader(reader, nil, clock...)
}

func NewWithRecentReader(reader InventoryReader, recent RecentReader, clock ...func() time.Time) *Service {
	now := time.Now
	if len(clock) > 0 && clock[0] != nil {
		now = clock[0]
	}
	return &Service{
		reader:      reader,
		recent:      recent,
		now:         now,
		cache:       make(map[string]*snapshot),
		loads:       make(map[string]*snapshotLoad),
		recentCache: make(map[string]*recentIndexEntry),
	}
}

func (s *Service) List(ctx context.Context, request Request) (Response, error) {
	if s == nil || s.reader == nil {
		return Response{}, errors.New("credential list is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	request = normalizeRequest(request)
	scope := "default"
	if scoped, ok := s.reader.(InventoryScopeReader); ok {
		resolved, err := scoped.AuthFileInventoryScope(ctx)
		if err != nil {
			return Response{}, err
		}
		if strings.TrimSpace(resolved) != "" {
			scope = strings.TrimSpace(resolved)
		}
	}
	key := scope + "\x00" + snapshotKey(request.IncludeProxyURL)
	current, err := s.ensureSnapshot(ctx, key, request)
	if err != nil {
		return Response{}, err
	}

	items := cloneItems(current.items)
	filtered := filterItems(items, request)
	if isRecentSort(request.Sort) && s.recent != nil {
		// Usage indexing is deliberately best-effort. A temporarily unavailable
		// SQLite read must not make the credential inventory disappear; callers
		// still receive the deterministic inventory order as a fallback. The
		// snapshot-level cache makes repeated page requests O(page) after the
		// first lookup rather than O(all credentials) SQLite work each time.
		_ = s.applyRecentRequestIndex(ctx, key, current, filtered)
	}
	sortItems(filtered, request.Sort)
	total := len(filtered)
	// A total hint from the unfiltered CPA inventory cannot be reused for a
	// search/provider/status query while the snapshot is still warming. Doing
	// so would make the browser render thousands of phantom search pages.
	if !current.complete && request.Search == "" && request.Provider == "" && request.Status == "" && total < current.totalHint {
		total = current.totalHint
	}
	start := (request.Page - 1) * request.PageSize
	pageItems := []map[string]any{}
	if start < len(filtered) {
		end := start + request.PageSize
		if end > len(filtered) {
			end = len(filtered)
		}
		pageItems = filtered[start:end]
	}
	status := "ready"
	if current.warming {
		status = "warming"
	} else if current.syncError != "" {
		status = "error"
	}
	age := int64(0)
	if !current.updatedAt.IsZero() {
		age = s.now().Sub(current.updatedAt).Milliseconds()
		if age < 0 {
			age = 0
		}
	}
	return Response{
		Files:                   pageItems,
		Total:                   total,
		InventoryTotal:          inventoryTotal(current),
		Page:                    request.Page,
		PageSize:                request.PageSize,
		HasMore:                 start+request.PageSize < total,
		ProxyURLIncluded:        current.proxyURLIncluded,
		SyncStatus:              status,
		SyncAgeMS:               age,
		SyncError:               current.syncError,
		ProxyURLs:               enabledProxyURLs(current.items),
		EnabledCredentialCount:  countEnabledItems(current.items),
		ProxyProjectionComplete: current.complete,
		ProviderCounts:          providerCounts(current.items),
	}, nil
}

func inventoryTotal(current *snapshot) int {
	if current == nil {
		return 0
	}
	if current.complete {
		return len(current.items)
	}
	if current.totalHint > len(current.items) {
		return current.totalHint
	}
	return len(current.items)
}

func providerCounts(items []map[string]any) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		provider := strings.ToLower(strings.TrimSpace(readString(item, "provider", "type")))
		if provider == "" {
			provider = "unknown"
		}
		counts[provider]++
	}
	return counts
}

func enabledProxyURLs(items []map[string]any) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, item := range items {
		if readBool(item, "disabled") {
			continue
		}
		proxy := strings.TrimSpace(readString(item, "proxy_url", "proxyUrl", "proxy-url"))
		if proxy == "" {
			continue
		}
		if _, ok := seen[proxy]; ok {
			continue
		}
		seen[proxy] = struct{}{}
		result = append(result, proxy)
	}
	return result
}

func countEnabledItems(items []map[string]any) int {
	count := 0
	for _, item := range items {
		if !readBool(item, "disabled") {
			count++
		}
	}
	return count
}

func isRecentSort(sortMode string) bool {
	return sortMode == "recent_desc" || sortMode == "recent_asc"
}

func (s *Service) applyRecentRequestIndex(
	ctx context.Context,
	cacheKey string,
	current *snapshot,
	items []map[string]any,
) error {
	if len(items) == 0 || s == nil || s.recent == nil || current == nil || current.warming {
		return nil
	}
	fingerprint := recentIndexFingerprint(current.items)
	for {
		now := s.now()
		s.mu.Lock()
		if s.recentCache == nil {
			s.recentCache = make(map[string]*recentIndexEntry)
		}
		entry := s.recentCache[cacheKey]
		if entry != nil && entry.fingerprint == fingerprint && entry.ready {
			ttl := recentIndexTTL
			if entry.err != nil {
				ttl = recentErrorTTL
			}
			if now.Sub(entry.updatedAt) < ttl {
				values := cloneRecentIndexValues(entry.values)
				err := entry.err
				s.mu.Unlock()
				applyRecentIndexValues(items, values)
				return err
			}
		}
		if entry != nil && entry.fingerprint == fingerprint && entry.loading {
			done := entry.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		entry = &recentIndexEntry{
			fingerprint: fingerprint,
			loading:     true,
			done:        make(chan struct{}),
		}
		s.recentCache[cacheKey] = entry
		s.mu.Unlock()

		values, err := s.queryRecentRequestIndex(ctx, current.items)
		s.mu.Lock()
		if currentEntry := s.recentCache[cacheKey]; currentEntry == entry {
			entry.values = values
			entry.updatedAt = s.now()
			entry.err = err
			entry.loading = false
			entry.ready = true
			close(entry.done)
		}
		s.mu.Unlock()
		applyRecentIndexValues(items, values)
		return err
	}
}

func (s *Service) queryRecentRequestIndex(
	ctx context.Context,
	items []map[string]any,
) (map[string]int64, error) {
	targets := make([]store.LatestAccountRequestQuery, 0, len(items))
	keys := make([]string, 0, len(items))
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
		keys = append(keys, itemKey(item))
	}
	requests, err := s.recent.RecentAccountRequests(ctx, targets, 1)
	values := make(map[string]int64, len(requests))
	if err != nil {
		return values, err
	}
	for _, request := range requests {
		if request.RequestIndex < 0 || request.RequestIndex >= len(keys) || request.TimestampMS <= 0 {
			continue
		}
		key := keys[request.RequestIndex]
		if previous := values[key]; request.TimestampMS > previous {
			values[key] = request.TimestampMS
		}
	}
	return values, nil
}

func applyRecentIndexValues(items []map[string]any, values map[string]int64) {
	for _, item := range items {
		if timestamp := values[itemKey(item)]; timestamp > 0 {
			item["last_request_ms"] = strconv.FormatInt(timestamp, 10)
		}
	}
}

func cloneRecentIndexValues(values map[string]int64) map[string]int64 {
	if len(values) == 0 {
		return nil
	}
	copy := make(map[string]int64, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func recentIndexFingerprint(items []map[string]any) string {
	identities := make([]string, 0, len(items))
	for _, item := range items {
		identities = append(identities, strings.Join([]string{
			itemKey(item),
			readString(item, "provider", "type"),
			readString(item, "chatgpt_account_id", "account_id", "accountId"),
			readString(item, "project_id", "projectId"),
			readString(item, "account_snapshot", "accountSnapshot", "email", "account"),
		}, "\x00"))
	}
	sort.Strings(identities)
	sum := sha256.Sum256([]byte(strings.Join(identities, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func (s *Service) ensureSnapshot(ctx context.Context, key string, request Request) (*snapshot, error) {
	now := s.now()
	s.mu.Lock()
	if s.loads == nil {
		s.loads = make(map[string]*snapshotLoad)
	}
	current := s.cache[key]
	if current != nil && current.complete && now.Sub(current.updatedAt) < snapshotTTL {
		copy := cloneSnapshot(current)
		s.mu.Unlock()
		return copy, nil
	}
	if current != nil && current.warming {
		copy := cloneSnapshot(current)
		s.mu.Unlock()
		return copy, nil
	}
	if pending := s.loads[key]; pending != nil {
		done := pending.done
		s.mu.Unlock()
		select {
		case <-done:
			if pending.err != nil {
				return nil, pending.err
			}
			return cloneSnapshot(pending.snapshot), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	pending := &snapshotLoad{done: make(chan struct{})}
	s.loads[key] = pending
	s.mu.Unlock()

	// Do not let a browser tab that disconnects cancel the shared first-page
	// fetch for every other waiter. The bounded CPA client timeout still limits
	// the request itself.
	loadCtx := context.WithoutCancel(ctx)
	body, err := s.reader.ReadAuthFileInventory(loadCtx, inventoryQuery(1, request.PageSize, request.IncludeProxyURL))
	if err != nil {
		s.mu.Lock()
		if currentLoad := s.loads[key]; currentLoad == pending {
			pending.err = err
			delete(s.loads, key)
			close(pending.done)
		}
		s.mu.Unlock()
		return nil, err
	}
	parsed, err := parseInventory(body, s.now)
	if err != nil {
		s.mu.Lock()
		if currentLoad := s.loads[key]; currentLoad == pending {
			pending.err = err
			delete(s.loads, key)
			close(pending.done)
		}
		s.mu.Unlock()
		return nil, err
	}
	parsed.fetchedAt = now
	parsed.updatedAt = now
	parsed.complete = !parsed.warming
	s.mu.Lock()
	copy := cloneSnapshot(parsed)
	accepted := false
	if currentLoad := s.loads[key]; currentLoad == pending {
		s.cache[key] = parsed
		pending.snapshot = cloneSnapshot(parsed)
		delete(s.loads, key)
		close(pending.done)
		accepted = true
	}
	s.mu.Unlock()
	if accepted && parsed.warming {
		go s.warmSnapshot(key, request.PageSize, request.IncludeProxyURL, parsed.totalHint)
	}
	return copy, nil
}

func (s *Service) warmSnapshot(key string, pageSize int, includeProxyURL bool, totalHint int) {
	for page := 2; page <= maxWarmPages; page++ {
		query := inventoryQuery(page, pageSize, includeProxyURL)
		body, err := s.reader.ReadAuthFileInventory(context.Background(), query)
		if err != nil {
			s.mu.Lock()
			if current := s.cache[key]; current != nil {
				current.warming = false
				current.syncError = safeSyncError(err)
			}
			s.mu.Unlock()
			return
		}
		parsed, err := parseInventory(body, s.now)
		if err != nil {
			s.mu.Lock()
			if current := s.cache[key]; current != nil {
				current.warming = false
				current.syncError = safeSyncError(err)
			}
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		current := s.cache[key]
		if current == nil {
			s.mu.Unlock()
			return
		}
		current.items = appendUniqueItems(current.items, parsed.items)
		if parsed.totalHint > current.totalHint {
			current.totalHint = parsed.totalHint
		}
		current.proxyURLIncluded = current.proxyURLIncluded || parsed.proxyURLIncluded
		current.updatedAt = s.now()
		// `total` is optional on CPA's paged response. Never stop merely
		// because the first page's item count was used as a provisional hint;
		// the upstream `has_more=false` marker is authoritative when no total
		// is available.
		done := !parsed.warming && (current.totalHint <= 0 || len(current.items) >= current.totalHint)
		if totalHint > 0 && len(current.items) >= totalHint && !parsed.warming {
			done = true
		}
		if done {
			current.warming = false
			current.complete = true
			current.syncError = ""
			current.updatedAt = s.now()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	if current := s.cache[key]; current != nil {
		current.warming = false
		current.complete = true
		current.syncError = "warm limit reached"
	}
	s.mu.Unlock()
}

func (s *Service) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cache = make(map[string]*snapshot)
	for key, pending := range s.loads {
		pending.err = errors.New("credential list snapshot invalidated")
		close(pending.done)
		delete(s.loads, key)
	}
	s.loads = make(map[string]*snapshotLoad)
	s.recentCache = make(map[string]*recentIndexEntry)
	s.mu.Unlock()
}

func normalizeRequest(request Request) Request {
	if request.Page < 1 {
		request.Page = 1
	}
	if request.PageSize < 1 {
		request.PageSize = defaultPageSize
	}
	if request.PageSize > maxPageSize {
		request.PageSize = maxPageSize
	}
	request.Search = strings.TrimSpace(strings.ToLower(request.Search))
	request.Provider = strings.TrimSpace(strings.ToLower(request.Provider))
	request.Status = strings.TrimSpace(strings.ToLower(request.Status))
	request.Sort = strings.TrimSpace(strings.ToLower(request.Sort))
	if request.Sort == "" {
		request.Sort = "name_asc"
	}
	return request
}

func inventoryQuery(page, pageSize int, includeProxyURL bool) url.Values {
	return url.Values{
		"page":              {strconv.Itoa(page)},
		"page_size":         {strconv.Itoa(pageSize)},
		"include_proxy_url": {strconv.Itoa(boolInt(includeProxyURL))},
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func snapshotKey(includeProxyURL bool) string {
	if includeProxyURL {
		return "proxy"
	}
	return "plain"
}

func cloneSnapshot(source *snapshot) *snapshot {
	if source == nil {
		return &snapshot{}
	}
	copy := *source
	copy.items = append([]map[string]any(nil), source.items...)
	return &copy
}

func cloneItems(source []map[string]any) []map[string]any {
	if len(source) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(source))
	for _, item := range source {
		copy := make(map[string]any, len(item))
		for key, value := range item {
			copy[key] = value
		}
		result = append(result, copy)
	}
	return result
}

func parseInventory(body []byte, clock ...func() time.Time) (*snapshot, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errors.New("CPA auth-file inventory is empty")
	}
	var envelope struct {
		Files            []map[string]any `json:"files"`
		Total            int              `json:"total"`
		HasMore          bool             `json:"has_more"`
		ProxyURLIncluded bool             `json:"proxy_url_included"`
	}
	if strings.HasPrefix(trimmed, "[") {
		var files []map[string]any
		if err := json.Unmarshal([]byte(trimmed), &files); err != nil {
			return nil, fmt.Errorf("decode CPA auth-file inventory: %w", err)
		}
		envelope.Files = files
		envelope.Total = len(files)
	} else if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return nil, fmt.Errorf("decode CPA auth-file inventory: %w", err)
	}
	items := make([]map[string]any, 0, len(envelope.Files))
	for _, raw := range envelope.Files {
		item := sanitizeItem(raw)
		if strings.TrimSpace(readString(item, "name")) == "" {
			continue
		}
		items = appendUniqueItems(items, []map[string]any{item})
	}
	total := envelope.Total
	if total < len(items) {
		total = len(items)
	}
	now := time.Now
	if len(clock) > 0 && clock[0] != nil {
		now = clock[0]
	}
	return &snapshot{
		items:            items,
		totalHint:        total,
		proxyURLIncluded: envelope.ProxyURLIncluded || hasProxyField(items),
		updatedAt:        now(),
		warming:          envelope.HasMore || total > len(items),
		complete:         !(envelope.HasMore || total > len(items)),
	}, nil
}

func appendUniqueItems(existing, additions []map[string]any) []map[string]any {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	for _, item := range existing {
		seen[itemKey(item)] = struct{}{}
	}
	for _, item := range additions {
		key := itemKey(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, item)
	}
	return existing
}

func itemKey(item map[string]any) string {
	return strings.Join([]string{readString(item, "name"), readString(item, "auth_index", "authIndex"), readString(item, "id")}, "\x00")
}

func filterItems(items []map[string]any, request Request) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if request.Provider != "" && request.Provider != "all" && !strings.EqualFold(request.Provider, readString(item, "provider", "type")) {
			continue
		}
		disabled := readBool(item, "disabled")
		switch request.Status {
		case "enabled", "enable":
			if disabled {
				continue
			}
		case "disabled", "disable":
			if !disabled {
				continue
			}
		}
		if request.Search != "" {
			searchable := strings.ToLower(strings.Join([]string{
				readString(item, "name", "email", "account", "note", "proxy_url", "proxyUrl"),
			}, " "))
			if !strings.Contains(searchable, request.Search) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func sortItems(items []map[string]any, sortMode string) {
	desc := sortMode == "name_desc" || sortMode == "recent_desc" || sortMode == "priority_desc"
	sort.SliceStable(items, func(left, right int) bool {
		var leftValue, rightValue string
		switch sortMode {
		case "recent_desc", "recent_asc":
			leftValue = recentSortValue(items[left])
			rightValue = recentSortValue(items[right])
		case "priority_desc", "priority_asc":
			leftValue = numericSortValue(items[left], "priority", "weight")
			rightValue = numericSortValue(items[right], "priority", "weight")
		default:
			leftValue = strings.ToLower(readString(items[left], "name", "email", "account"))
			rightValue = strings.ToLower(readString(items[right], "name", "email", "account"))
		}
		if leftValue == rightValue {
			return itemKey(items[left]) < itemKey(items[right])
		}
		if desc {
			return leftValue > rightValue
		}
		return leftValue < rightValue
	})
}

func numericSortValue(item map[string]any, keys ...string) string {
	value := readString(item, keys...)
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return value
	}
	return fmt.Sprintf("%020.6f", parsed)
}

func recentSortValue(item map[string]any) string {
	for _, key := range []string{"last_request_ms", "last_request_at", "latest_request_at"} {
		if value := readString(item, key); value != "" {
			return value
		}
	}
	return ""
}

func sanitizeItem(item map[string]any) map[string]any {
	clean := make(map[string]any, len(item))
	for key, value := range item {
		if sensitiveInventoryField(key) {
			continue
		}
		clean[key] = sanitizeValue(value)
	}
	if proxy := readString(clean, "proxyUrl", "proxy-url"); readString(clean, "proxy_url") == "" && proxy != "" {
		clean["proxy_url"] = proxy
	}
	return clean
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeItem(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeValue(item)
		}
		return result
	default:
		return value
	}
}

func sensitiveInventoryField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "id_token", "refresh_token", "token", "tokens", "authorization", "api_key", "apikey", "client_secret", "secret", "password", "cookie", "headers", "credential", "credentials":
		return true
	default:
		return false
	}
}

func readString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case float64:
				return strconv.FormatFloat(typed, 'f', -1, 64)
			case json.Number:
				return typed.String()
			}
		}
	}
	return ""
}

func readBool(item map[string]any, key string) bool {
	value, ok := item[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func hasProxyField(items []map[string]any) bool {
	for _, item := range items {
		if _, ok := item["proxy_url"]; ok {
			return true
		}
	}
	return false
}

func safeSyncError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		return message[:240]
	}
	return message
}
