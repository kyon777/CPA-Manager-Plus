package model

import "testing"

func TestNormalizeProxyURLAndFingerprint(t *testing.T) {
	if got := NormalizeProxyURL("  http://u:p@host:8080/// "); got != "http://u:p@host:8080" {
		t.Fatalf("normalized = %q", got)
	}
	if ProxyFingerprint("http://u:p@host:8080/") != ProxyFingerprint("http://u:p@host:8080") {
		t.Fatal("trailing slash must not change fingerprint")
	}
	if got := ProxyFingerprint("   "); got != "" {
		t.Fatalf("empty proxy fingerprint = %q, want empty", got)
	}
}

func TestIsSuspectedProxyTransportFailure(t *testing.T) {
	yes := []ProxyFailureEvidence{
		{
			ProxyURL:    "http://p:1",
			ErrorKind:   "request_error",
			ErrorDetail: `api-call failed: 502 Bad Gateway {"error":"request failed"}`,
		},
		{
			ProxyURL:    "http://p:1",
			ErrorKind:   "request_error",
			ErrorDetail: "Get https://example.test: context deadline exceeded",
		},
	}
	for _, evidence := range yes {
		if !IsSuspectedProxyTransportFailure(evidence) {
			t.Fatalf("expected suspected proxy transport failure: %#v", evidence)
		}
	}

	no := []ProxyFailureEvidence{
		{ProxyURL: "http://p:1", StatusCode: 401, ErrorKind: "http_status"},
		{ProxyURL: "http://p:1", StatusCode: 429, ErrorKind: "http_status", ErrorDetail: `{"detail":"Rate limit exceeded"}`},
		{ProxyURL: "http://p:1", ErrorKind: "http_status", ErrorDetail: "HTTP 503 upstream business response"},
		{ProxyURL: "http://p:1", ErrorKind: "request_error", ErrorDetail: "credits exhausted"},
		{ProxyURL: "", ErrorKind: "request_error", ErrorDetail: "context deadline exceeded"},
	}
	for _, evidence := range no {
		if IsSuspectedProxyTransportFailure(evidence) {
			t.Fatalf("unexpected suspected proxy transport failure: %#v", evidence)
		}
	}
}
