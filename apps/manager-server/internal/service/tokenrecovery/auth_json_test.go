package tokenrecovery

import (
	"errors"
	"strings"
	"testing"
)

func TestMergeAuthJSONUpdatesOnlySelectedArrayRecordAndPreservesFields(t *testing.T) {
	source := []byte(`[
  {"auth_index":"one","email":"sibling@example.com","access_token":"sibling-access","note":"sibling"},
  {
    "auth_index":"two",
    "email":"person@example.com",
    "access_token":"old-access",
    "refresh_token":"old-refresh",
    "id_token":"old-id",
    "account_id":"old-workspace",
    "chatgptAccountId":"old-workspace",
    "note":"keep this note",
    "priority":20000,
    "proxy_url":"http://proxy-user:proxy-pass@proxy.example:8080",
    "unknown":{"keep":true}
  }
]`)
	locator := Locator{AuthIndex: "two", AccountEmail: "person@example.com"}
	result := AcquisitionResult{
		Email:            "PERSON@example.com",
		AccessToken:      "new-access",
		RefreshToken:     "new-refresh",
		IDToken:          "new-id-token",
		ChatGPTAccountID: "new-workspace",
	}

	credential, err := ReadCredential(source, locator)
	if err != nil {
		t.Fatalf("ReadCredential() error = %v", err)
	}
	if credential.Email != "person@example.com" {
		t.Fatalf("credential email = %q", credential.Email)
	}
	if credential.HTTPProxy != "http://proxy-user:proxy-pass@proxy.example:8080" {
		t.Fatalf("credential HTTPProxy = %q", credential.HTTPProxy)
	}

	merged, err := MergeAuthJSON(source, locator, result)
	if err != nil {
		t.Fatalf("MergeAuthJSON() error = %v", err)
	}
	text := string(merged)
	for _, want := range []string{
		`"note":"keep this note"`,
		`"priority":20000`,
		`"proxy_url":"http://proxy-user:proxy-pass@proxy.example:8080"`,
		`"unknown":{"keep":true}`,
		`"access_token":"new-access"`,
		`"refresh_token":"new-refresh"`,
		`"id_token":"new-id-token"`,
		`"chatgpt_account_id":"new-workspace"`,
		`"account_id":"new-workspace"`,
		`"chatgptAccountId":"new-workspace"`,
		`"access_token":"sibling-access"`,
		`"note":"sibling"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("merged JSON does not preserve/write %s: %s", want, text)
		}
	}
	if err := VerifyMergedAuthJSON(merged, locator, result); err != nil {
		t.Fatalf("VerifyMergedAuthJSON() error = %v", err)
	}
}

func TestMergeAuthJSONRejectsEmailMismatchWithoutOutput(t *testing.T) {
	source := []byte(`{"auth_index":"1","email":"person@example.com","access_token":"old","refresh_token":"old","id_token":"old"}`)
	merged, err := MergeAuthJSON(source, Locator{AuthIndex: "1"}, AcquisitionResult{
		Email: "other@example.com", AccessToken: "access", RefreshToken: "refresh", IDToken: "id",
	})
	if !errors.Is(err, ErrCredentialEmailMismatch) {
		t.Fatalf("MergeAuthJSON() error = %v, want ErrCredentialEmailMismatch", err)
	}
	if merged != nil {
		t.Fatalf("MergeAuthJSON() output = %q, want nil on mismatch", merged)
	}
}

func TestReadCredentialRejectsAmbiguousEmailOnlyArrayTarget(t *testing.T) {
	source := []byte(`[
  {"auth_index":"one","email":"person@example.com"},
  {"auth_index":"two","email":"person@example.com"}
]`)
	_, err := ReadCredential(source, Locator{AccountEmail: "person@example.com"})
	if !errors.Is(err, ErrCredentialAmbiguous) {
		t.Fatalf("ReadCredential() error = %v, want ErrCredentialAmbiguous", err)
	}
}

func TestMergeAuthJSONRejectsIncompleteResultAndNonHTTPProxy(t *testing.T) {
	source := []byte(`{"email":"person@example.com","proxy_url":"socks5://127.0.0.1:1080"}`)
	credential, err := ReadCredential(source, Locator{AccountEmail: "person@example.com"})
	if err != nil {
		t.Fatalf("ReadCredential() error = %v", err)
	}
	if credential.HTTPProxy != "" {
		t.Fatalf("non-HTTP proxy forwarded as %q", credential.HTTPProxy)
	}
	_, err = MergeAuthJSON(source, Locator{AccountEmail: "person@example.com"}, AcquisitionResult{
		Email: "person@example.com", AccessToken: "access", RefreshToken: "", IDToken: "id",
	})
	if !errors.Is(err, ErrIncompleteTokenResult) {
		t.Fatalf("MergeAuthJSON() error = %v, want ErrIncompleteTokenResult", err)
	}
}
