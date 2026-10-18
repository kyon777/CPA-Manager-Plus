package cpaauthfiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPatchProxyURLTargetSendsOnlyAllowedFields(t *testing.T) {
	received := map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != authFilesFieldsPath {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	target := StatusMutationTarget{
		Selector: "runtime-7",
		Scope:    StatusMutationScopeCredential,
		File:     File{ID: "runtime-7", Name: "alice.json"},
	}
	if err := New(server.Client()).PatchProxyURLTarget(
		context.Background(), server.URL, "key", target, " http://user:pass@proxy:8080/// ",
	); err != nil {
		t.Fatalf("patch proxy: %v", err)
	}
	want := map[string]any{"name": "runtime-7", "proxy_url": "http://user:pass@proxy:8080"}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("fields payload = %#v, want %#v", received, want)
	}
}

func TestPatchProxyURLTargetRejectsSourceFileMutation(t *testing.T) {
	target := StatusMutationTarget{
		Selector: "source.json",
		Scope:    StatusMutationScopeSourceFile,
		File:     File{ID: "runtime-7", Name: "source.json"},
	}
	err := New(nil).PatchProxyURLTarget(context.Background(), "http://unused", "key", target, "http://proxy:8080")
	if !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
}

func TestRewriteProxyURLPreservesAllNonProxyFields(t *testing.T) {
	raw := []byte(`[{"auth_index":"7","email":"a@example.com","note":"keep","priority":5,"refresh_token":"rt","headers":{"x":"y"},"proxy_url":"http://old"}]`)
	next, err := RewriteProxyURL(raw, ProxyRecordLocator{AuthIndex: "7", AccountEmail: "a@example.com"}, "http://old", "http://new/")
	if err != nil {
		t.Fatalf("rewrite proxy: %v", err)
	}
	var records []map[string]any
	if err := json.Unmarshal(next, &records); err != nil {
		t.Fatalf("decode rewritten JSON: %v", err)
	}
	if len(records) != 1 || records[0]["note"] != "keep" || records[0]["priority"] != float64(5) || records[0]["refresh_token"] != "rt" {
		t.Fatalf("non-proxy fields changed: %#v", records)
	}
	if !reflect.DeepEqual(records[0]["headers"], map[string]any{"x": "y"}) || records[0]["proxy_url"] != "http://new" {
		t.Fatalf("rewritten record = %#v", records[0])
	}
}

func TestRewriteProxyURLRequiresUniqueArrayAuthIndexAndExpectedOldValue(t *testing.T) {
	raw := []byte(`[{"auth_index":"7","email":"a@example.com","proxy_url":"http://old"},{"auth_index":"8","email":"b@example.com","proxy_url":"http://other"}]`)
	if _, err := RewriteProxyURL(raw, ProxyRecordLocator{AccountEmail: "a@example.com"}, "http://old", "http://new"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("missing array auth index error = %v", err)
	}
	if _, err := RewriteProxyURL(raw, ProxyRecordLocator{AuthIndex: "7", AccountEmail: "a@example.com"}, "http://changed", "http://new"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("stale old URL error = %v", err)
	}
}

func TestFetchWithProxyURLsRequestsProxyProjection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != authFilesPath || r.URL.Query().Get("include_proxy_url") != "1" {
			t.Fatalf("request = %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{
			"id": "runtime-1", "name": "one.json", "proxy_url": "http://user:secret@proxy:8080",
		}}})
	}))
	defer server.Close()
	files, err := New(server.Client()).FetchWithProxyURLs(context.Background(), server.URL, "key")
	if err != nil || len(files) != 1 || files[0].Raw["proxy_url"] != "http://user:secret@proxy:8080" {
		t.Fatalf("files=%#v err=%v", files, err)
	}
}

func TestResolveProxyURLMutationTargetRequestsProxyProjection(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != authFilesPath {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("include_proxy_url"); got != "1" {
			t.Fatalf("include_proxy_url = %q, want 1 (request %s)", got, r.URL.String())
		}
		if r.URL.Query().Get("name") == "runtime-7" && r.URL.Query().Get("auth_index") != "7" {
			got := r.URL.Query().Get("auth_index")
			t.Fatalf("auth_index = %q, want 7 (request %s)", got, r.URL.String())
		}
		requests = append(requests, r.URL.Query().Get("name"))
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{
			"id": "runtime-7", "name": "alice.json", "auth_index": "7", "provider": "codex", "proxy_url": "http://old-proxy:8080",
		}}})
	}))
	defer server.Close()

	target, err := New(server.Client()).ResolveProxyURLMutationTarget(
		context.Background(), server.URL, "key", "runtime-7", "7",
	)
	if err != nil {
		t.Fatalf("resolve proxy mutation target: %v", err)
	}
	if got := target.File.Raw["proxy_url"]; got != "http://old-proxy:8080" {
		t.Fatalf("proxy_url = %#v, want projected current proxy", got)
	}
	if len(requests) == 0 || requests[0] != "runtime-7" {
		t.Fatalf("targeted lookup requests = %#v", requests)
	}
}
