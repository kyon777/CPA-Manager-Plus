package model

import "strings"

// ProxyFilterSettings stores the server-side proxy inventory used by the
// credential-list missing-proxy filter.
type ProxyFilterSettings struct {
	URLs        []string `json:"urls"`
	UpdatedAtMS int64    `json:"updatedAtMs,omitempty"`
}

// NormalizeProxyFilterURLs removes empty entries, surrounding whitespace,
// trailing URL slashes, and duplicates while preserving first-seen order.
func NormalizeProxyFilterURLs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		canonical := strings.TrimSpace(value)
		canonical = strings.TrimRight(canonical, "/")
		if canonical == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result
}
