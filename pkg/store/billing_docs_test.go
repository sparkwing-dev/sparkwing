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
	var rungs []string
	for _, row := range billingPolicy {
		rungs = append(rungs, fmt.Sprintf("$%d", row.rungCents/100))
		if row.tier == "" {
			continue
		}
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
	}
	if !strings.Contains(body, strings.Join(rungs, " → ")) {
		t.Error("billing documentation differs from the collection ladder")
	}
}
