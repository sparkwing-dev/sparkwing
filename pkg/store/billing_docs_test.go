package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBillingModelDocumentationTracksPolicy(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "docs", "billing.md"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), ",", "")
	for _, term := range []string{fmt.Sprintf("at most $%d", MaxCardPaymentCents/100), "including High", "larger payments", "operator console queue", "authenticated", "attempt_acknowledged", "three_d_secure", "liability_shift", "unprotected", "frozen", "cancels the metered job", "owner keeps orchestrating", "independent unmetered jobs continue", "whole run stops", "customer-owned runners", "dependency settlement"} {
		if !strings.Contains(body, term) {
			t.Errorf("billing documentation omits %q", term)
		}
	}
	for _, row := range billingPolicy {
		prefix := fmt.Sprintf("| %s | %d | %d hours |", row.tier, row.vCPU, int(row.duration.Hours()))
		if !strings.Contains(body, prefix) {
			t.Errorf("billing documentation is missing policy row %q", prefix)
		}
		if row.cleanAge > 0 {
			line, _, _ := strings.Cut(body[strings.Index(body, prefix)+len(prefix):], "\n")
			if days := fmt.Sprintf("%d clean days", int(row.cleanAge.Hours()/24)); !strings.Contains(line, days) {
				t.Errorf("billing documentation row %q omits %q", prefix, days)
			}
		}
		if row.refillCents > 0 {
			caps := fmt.Sprintf("| %s | $%d | %d | $%d |", row.tier, row.refillCents/100, row.refillsPerDay, row.refillWeekCents/100)
			if !strings.Contains(body, caps) {
				t.Errorf("billing documentation is missing refill caps row %q", caps)
			}
		}
	}
}
