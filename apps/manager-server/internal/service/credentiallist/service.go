package credentiallist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPageSize = 50
	maxPageSize     = 50
	maxWarmPages    = 2000
	snapshotTTL     = 30 * time.Second
)

// InventoryReader is deliberately limited to the CPA management inventory
// endpoint. It cannot issue model requests or touch usage tables.
type InventoryReader interface {
	ReadAuthFileInventory(context.Context, url.Values) ([]byte, error)
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
	Page             int              `json:"page"`
	PageSize         int              `json:"page_size"`
	HasMore          bool             `json:"has_more"`
	ProxyURLIncluded bool             `json:"proxy_url_included"`
	SyncStatus       string           `json:"sync_status"`
	SyncAgeMS        int64            `json:"sync_age_ms,omitempty"`
	SyncError        string           `json:"sync_error,omitempty"`
}

type Service struct {
	reader InventoryReader
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]*snapshot
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

func New(reader InventoryReader, clock ...func() time.Time) *Service {
	now := time.Now
	if len(clock) > 0 && clock[0] != nil {
		now = clock[0]
	}
	return &Service{reader: reader, now: now, cache: make(map[string]*snapshot)}
}

func (s *Service) List(ctx context.Context, request Request) (Response, error) {
	if s == nil || s.reader == nil {
		return Response{}, errors.New("credential list is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	request = normalizeRequest(request)
	key := snapshotKey(request.IncludeProxyURL)
	current, err := s.ensureSnapshot(ctx, key, request)
	if err != nil {
		return Response{}, err
	}

	items := append([]map[string]any(nil), current.items...)
	filtered := filterItems(items, request)
	sortItems(filtered, request.Sort)
	total := len(filtered)
	if !current.complete && total < current.totalHint {
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
		Files:            pageItems,
		Total:            total,
		Page:             request.Page,
		PageSize:         request.PageSize,
		HasMore:          start+request.PageSize < total,
		ProxyURLIncluded: current.proxyURLIncluded,
		SyncStatus:       status,
		SyncAgeMS:        age,
		SyncError:        current.syncError,
	}, nil
}

func (s *Service) ensureSnapshot(ctx context.Context, key string, request Request) (*snapshot, error) {
	now := s.now()
	s.mu.Lock()
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
	s.mu.Unlock()

	body, err := s.reader.ReadAuthFileInventory(ctx, inventoryQuery(1, request.PageSize, request.IncludeProxyURL))
	if err != nil {
		return nil, err
	}
	parsed, err := parseInventory(body)
	if err != nil {
		return nil, err
	}
	parsed.fetchedAt = now
	parsed.updatedAt = now
	parsed.complete = !parsed.warming
	s.mu.Lock()
	s.cache[key] = parsed
	copy := cloneSnapshot(parsed)
	s.mu.Unlock()
	if parsed.warming {
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
		parsed, err := parseInventory(body)
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
		done := !parsed.warming && len(current.items) >= current.totalHint
		if totalHint > 0 && len(current.items) >= totalHint {
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

func parseInventory(body []byte) (*snapshot, error) {
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
	return &snapshot{
		items:            items,
		totalHint:        total,
		proxyURLIncluded: envelope.ProxyURLIncluded || hasProxyField(items),
		updatedAt:        time.Now(),
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
			leftValue = readString(items[left], "priority", "weight")
			rightValue = readString(items[right], "priority", "weight")
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

func recentSortValue(item map[string]any) string {
	for _, key := range []string{"last_request_at", "last_request_ms", "latest_request_at"} {
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
