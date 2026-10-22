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

func TestProxyPoolPendingVerificationIsDistinctFromSuspect(t *testing.T) {
	if ProxyPoolStatusPendingVerification == ProxyPoolStatusSuspect {
		t.Fatal("pending verification must not alias the allocation-excluding suspect state")
	}
	if got := string(ProxyPoolStatusPendingVerification); got != "pending_verification" {
		t.Fatalf("pending verification value = %q", got)
	}
}

func TestProxyReplacementRecheckInconclusiveIsDistinct(t *testing.T) {
	if ProxyReplacementItemRecheckInconclusive == ProxyReplacementItemRecheckFailed {
		t.Fatal("inconclusive must not be represented as a direct proxy failure")
	}
}

func TestCredentialExitIPCheckResultKeepsOnlyLocatorAndDirectOutcome(t *testing.T) {
	got := CredentialProxyExitIPCheckResult{
		ClientKey: "row-1",
		Outcome:   ProxyExitIPOutcomeProxyFailed,
		Reason:    "proxy_connect_timeout",
	}
	if got.ClientKey != "row-1" || got.Outcome != ProxyExitIPOutcomeProxyFailed {
		t.Fatalf("result=%#v", got)
	}
}
