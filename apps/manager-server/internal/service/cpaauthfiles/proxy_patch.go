package cpaauthfiles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// ProxyRecordLocator narrows an in-file credential record. Array records must
// carry a stable AuthIndex; a single object can be additionally guarded by its
// account email because it has no sibling ambiguity.
type ProxyRecordLocator struct {
	AuthIndex    string
	AccountEmail string
}

// PatchProxyURLTarget is the only direct field-patch primitive exposed for
// proxy replacement. It intentionally accepts no caller-supplied field map.
func (c *Client) PatchProxyURLTarget(
	ctx context.Context,
	baseURL string,
	managementKey string,
	target StatusMutationTarget,
	proxyURL string,
) error {
	if target.Scope != StatusMutationScopeCredential {
		return fmt.Errorf("PATCH %s preflight: %w: target scope is %q", authFilesFieldsPath, ErrStatusMutationScopeAmbiguous, target.Scope)
	}
	selector := strings.TrimSpace(target.File.ID)
	if selector == "" {
		return fmt.Errorf("PATCH %s preflight: %w: mutation target has no stable runtime id", authFilesFieldsPath, ErrStatusMutationScopeAmbiguous)
	}
	normalizedProxyURL := model.NormalizeProxyURL(proxyURL)
	if normalizedProxyURL == "" {
		return fmt.Errorf("PATCH %s preflight: proxy URL is empty", authFilesFieldsPath)
	}
	payload := map[string]any{"name": selector, "proxy_url": normalizedProxyURL}
	if authIndex := strings.TrimSpace(target.File.AuthIndex); authIndex != "" {
		payload["auth_index"] = authIndex
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal proxy URL payload: %w", err)
	}
	base := cpa.NormalizeBaseURL(baseURL)
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, base+authFilesFieldsPath, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesFieldsPath, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+managementKey)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", authFilesFieldsPath, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= http.StatusOK && res.StatusCode < http.StatusMultipleChoices {
		if err := ValidateActionResponse(res.Body); err != nil {
			return fmt.Errorf("PATCH %s: %w", authFilesFieldsPath, err)
		}
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return &actionHTTPError{
		method:     http.MethodPatch,
		path:       authFilesFieldsPath,
		statusCode: res.StatusCode,
		body:       strings.TrimSpace(string(body)),
	}
}

// ReadProxyURLFromJSON extracts the selected proxy URL without exposing
// unrelated credential fields to callers.
func ReadProxyURLFromJSON(raw []byte, locator ProxyRecordLocator) (string, error) {
	record, _, _, _, err := locateProxyRecord(raw, locator)
	if err != nil {
		return "", err
	}
	return model.NormalizeProxyURL(rawString(record["proxy_url"])), nil
}

// RewriteProxyURL changes only the selected record's proxy_url. The expected
// old proxy is a compare-and-swap guard: any external mutation makes the
// operation fail closed with ErrIdentityMismatch rather than overwrite it.
func RewriteProxyURL(raw []byte, locator ProxyRecordLocator, expectedOldProxy, nextProxy string) ([]byte, error) {
	root, isArray, records, index, err := locateProxyRecord(raw, locator)
	if err != nil {
		return nil, err
	}
	expected := model.NormalizeProxyURL(expectedOldProxy)
	if model.NormalizeProxyURL(rawString(records[index]["proxy_url"])) != expected {
		return nil, fmt.Errorf("%w: proxy URL changed", ErrIdentityMismatch)
	}
	next := model.NormalizeProxyURL(nextProxy)
	if next == "" {
		return nil, fmt.Errorf("%w: proxy URL is empty", ErrIdentityMismatch)
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	records[index]["proxy_url"] = nextRaw
	if !isArray {
		return json.Marshal(root)
	}
	items := make([]json.RawMessage, len(records))
	for itemIndex, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		items[itemIndex] = encoded
	}
	return json.Marshal(items)
}

// locateProxyRecord returns a raw-message map so non-proxy values preserve
// their original JSON representation during a rewrite.
func locateProxyRecord(raw []byte, locator ProxyRecordLocator) (map[string]json.RawMessage, bool, []map[string]json.RawMessage, int, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil, 0, fmt.Errorf("%w: auth JSON is empty", ErrIdentityMismatch)
	}
	switch trimmed[0] {
	case '{':
		var record map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &record); err != nil {
			return nil, false, nil, 0, fmt.Errorf("decode auth JSON object: %w", err)
		}
		if !matchesProxyLocator(record, locator, false) {
			return nil, false, nil, 0, fmt.Errorf("%w: auth JSON object does not match locator", ErrIdentityMismatch)
		}
		return record, false, []map[string]json.RawMessage{record}, 0, nil
	case '[':
		if strings.TrimSpace(locator.AuthIndex) == "" {
			return nil, true, nil, 0, fmt.Errorf("%w: auth index is required for an auth JSON array", ErrIdentityMismatch)
		}
		var rawRecords []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawRecords); err != nil {
			return nil, true, nil, 0, fmt.Errorf("decode auth JSON array: %w", err)
		}
		records := make([]map[string]json.RawMessage, 0, len(rawRecords))
		matches := make([]int, 0, 1)
		for rawIndex, encoded := range rawRecords {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &record); err != nil {
				return nil, true, nil, 0, fmt.Errorf("decode auth JSON array record: %w", err)
			}
			records = append(records, record)
			if matchesProxyLocator(record, locator, true) {
				matches = append(matches, rawIndex)
			}
		}
		if len(matches) != 1 {
			return nil, true, nil, 0, fmt.Errorf("%w: auth JSON array locator is missing or ambiguous", ErrIdentityMismatch)
		}
		return records[matches[0]], true, records, matches[0], nil
	default:
		return nil, false, nil, 0, fmt.Errorf("%w: auth JSON root must be object or array", ErrIdentityMismatch)
	}
}

func matchesProxyLocator(record map[string]json.RawMessage, locator ProxyRecordLocator, requireAuthIndex bool) bool {
	authIndex := strings.TrimSpace(locator.AuthIndex)
	if requireAuthIndex && authIndex == "" {
		return false
	}
	if authIndex != "" && rawString(record["auth_index"]) != authIndex {
		return false
	}
	wantEmail := normalizeProxyEmail(locator.AccountEmail)
	if wantEmail == "" {
		return authIndex != ""
	}
	return normalizeProxyEmail(readProxyRecordEmail(record)) == wantEmail
}

func readProxyRecordEmail(record map[string]json.RawMessage) string {
	for _, key := range []string{"email", "account", "account_email", "accountEmail"} {
		if value := rawString(record[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"user", "account"} {
		var nested map[string]json.RawMessage
		if raw := record[key]; len(raw) > 0 && json.Unmarshal(raw, &nested) == nil {
			if value := rawString(nested["email"]); value != "" {
				return value
			}
		}
	}
	return ""
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func normalizeProxyEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
