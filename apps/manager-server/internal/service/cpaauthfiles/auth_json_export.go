package cpaauthfiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrAuthJSONInvalid means a downloaded auth-file source cannot be safely
// interpreted as one JSON object or an array of JSON objects.
var ErrAuthJSONInvalid = errors.New("auth JSON source is invalid")

// AuthJSONRecordLocator narrows an exported logical credential record. Array
// sources always require AuthIndex so selecting one member can never fall back
// to exporting its unselected siblings. AccountEmail is an additional guard.
type AuthJSONRecordLocator struct {
	AuthIndex    string
	AccountEmail string
}

// ExtractAuthJSONRecord returns exactly one selected auth JSON object from a
// raw CPA auth-file source. It deliberately does not marshal into a broad
// struct, preserving all original credential fields while never exposing a
// sibling from a shared array source.
func ExtractAuthJSONRecord(raw []byte, locator AuthJSONRecordLocator) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: source is empty", ErrAuthJSONInvalid)
	}

	switch trimmed[0] {
	case '{':
		var record map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &record); err != nil || record == nil {
			return nil, fmt.Errorf("%w: decode object", ErrAuthJSONInvalid)
		}
		if !matchesAuthJSONObjectLocator(record, locator) {
			return nil, fmt.Errorf("%w: object locator mismatch", ErrIdentityMismatch)
		}
		return append(json.RawMessage(nil), trimmed...), nil
	case '[':
		if strings.TrimSpace(locator.AuthIndex) == "" {
			return nil, fmt.Errorf("%w: auth index is required for an array", ErrIdentityMismatch)
		}

		var records []json.RawMessage
		if err := json.Unmarshal(trimmed, &records); err != nil || records == nil {
			return nil, fmt.Errorf("%w: decode array", ErrAuthJSONInvalid)
		}

		matches := make([]json.RawMessage, 0, 1)
		for _, encoded := range records {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &record); err != nil || record == nil {
				return nil, fmt.Errorf("%w: decode array member", ErrAuthJSONInvalid)
			}
			if matchesAuthJSONArrayLocator(record, locator) {
				matches = append(matches, append(json.RawMessage(nil), encoded...))
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("%w: array locator is missing or ambiguous", ErrIdentityMismatch)
		}
		return matches[0], nil
	default:
		return nil, fmt.Errorf("%w: root must be object or array", ErrAuthJSONInvalid)
	}
}

func matchesAuthJSONObjectLocator(record map[string]json.RawMessage, locator AuthJSONRecordLocator) bool {
	if expected := strings.TrimSpace(locator.AuthIndex); expected != "" {
		if actual := strings.TrimSpace(rawString(record["auth_index"])); actual != "" && actual != expected {
			return false
		}
	}
	if expected := normalizeAuthJSONEmail(locator.AccountEmail); expected != "" {
		if actual := normalizeAuthJSONEmail(readAuthJSONRecordEmail(record)); actual != "" && actual != expected {
			return false
		}
	}
	return true
}

func matchesAuthJSONArrayLocator(record map[string]json.RawMessage, locator AuthJSONRecordLocator) bool {
	if strings.TrimSpace(rawString(record["auth_index"])) != strings.TrimSpace(locator.AuthIndex) {
		return false
	}
	expectedEmail := normalizeAuthJSONEmail(locator.AccountEmail)
	return expectedEmail == "" || normalizeAuthJSONEmail(readAuthJSONRecordEmail(record)) == expectedEmail
}

func readAuthJSONRecordEmail(record map[string]json.RawMessage) string {
	for _, key := range []string{"email", "account_email", "accountEmail"} {
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

func normalizeAuthJSONEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
