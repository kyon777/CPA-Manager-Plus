package cpaauthfiles

import (
	"errors"
	"testing"
)

func TestExtractAuthJSONRecordSelectsOnlyRequestedArrayMember(t *testing.T) {
	raw := []byte(`[{"auth_index":"7","email":"a@example.com","refresh_token":"rt-a","note":"keep-a"},{"auth_index":"8","email":"b@example.com","refresh_token":"rt-b","note":"keep-b"}]`)

	record, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{
		AuthIndex: "8", AccountEmail: "b@example.com",
	})
	if err != nil {
		t.Fatalf("ExtractAuthJSONRecord() error = %v", err)
	}
	want := `{"auth_index":"8","email":"b@example.com","refresh_token":"rt-b","note":"keep-b"}`
	if string(record) != want {
		t.Fatalf("record = %s, want %s", record, want)
	}
}

func TestExtractAuthJSONRecordRejectsMissingArrayIndexAndInvalidJSON(t *testing.T) {
	if _, err := ExtractAuthJSONRecord(
		[]byte(`[{"auth_index":"7","email":"a@example.com"}]`),
		AuthJSONRecordLocator{AccountEmail: "a@example.com"},
	); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing array auth index error = %v", err)
	}
	if _, err := ExtractAuthJSONRecord([]byte(`{`), AuthJSONRecordLocator{}); !errors.Is(err, ErrAuthJSONInvalid) {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func TestExtractAuthJSONRecordAcceptsSingleObjectAndGuardsPresentEmail(t *testing.T) {
	raw := []byte(`{"type":"codex","user":{"email":"A@example.com"},"refresh_token":"rt","proxy_url":"http://p"}`)
	record, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{AuthIndex: "runtime-only", AccountEmail: "a@example.com"})
	if err != nil || string(record) != string(raw) {
		t.Fatalf("record=%s err=%v", record, err)
	}
	if _, err := ExtractAuthJSONRecord(raw, AuthJSONRecordLocator{AccountEmail: "other@example.com"}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("mismatched object email error = %v", err)
	}
}
