package usagepricing

import "testing"

func TestHistoryAccountRowsSkipRawTailDuringRebuild(t *testing.T) {
	for _, status := range []string{"rebuilding", "clearing"} {
		if !historyAccountRowsMaySkipRawTail(State{Status: status}) {
			t.Fatalf("status %q should skip the pricing raw tail for account-history reads", status)
		}
	}
	for _, status := range []string{"ready", "catching_up", "pending", "failed"} {
		if historyAccountRowsMaySkipRawTail(State{Status: status}) {
			t.Fatalf("status %q must retain the pricing raw tail for account-history reads", status)
		}
	}
}
