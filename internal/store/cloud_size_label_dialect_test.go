package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// The claim query's funding check sizes a job by the same label rule the Go award prices it with. PostgreSQL read
// substr(label,-3) as the whole label, so a sparkwing-8cpu job passed the funding check at the smallest size and
// failed its award on every poll.
func TestCloudSizeLabelSQLAgreesWithGoInEachDialect(t *testing.T) {
	labels := []string{"sparkwing-8cpu", "sparkwing-64cpu", "sparkwing-cpu", "sparkwing-", "sparkwing-gpu", "linux", "cpu", "ab", "", "x-8cpu", "Sparkwing-8cpu"}
	bothDialects(t, func(t *testing.T, target *storetest.Target) {
		db := target.Open(t).DB()
		for _, label := range labels {
			var got bool
			expr := store.CloudSizeLabelSQL(`CAST('` + label + `' AS TEXT)`)
			if err := db.QueryRowContext(t.Context(), `SELECT CASE WHEN `+expr+` THEN 1=1 ELSE 1=0 END`).Scan(&got); err != nil {
				t.Fatalf("%q: %v", label, err)
			}
			if want := match.IsCloudSizeLabel(label); got != want {
				t.Errorf("label %q: SQL says size %v, Go says %v", label, got, want)
			}
		}
	})
}
