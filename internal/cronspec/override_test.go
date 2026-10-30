package cronspec_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
)

func TestOverrideCronErrorNamesFieldAndExample(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cron string
		want string
	}{
		{"61 * * * *", "minute field"},
		{"0 24 * * *", "hour field"},
		{"0 0 32 * *", "day-of-month field"},
		{"0 0 * 13 *", "month field"},
		{"0 0 * * 8", "day-of-week field"},
		{"* * * *", "expected five fields"},
		{"@bogus", "unknown alias"},
	} {
		t.Run(tc.cron, func(t *testing.T) {
			err := (cronspec.Override{Cron: &tc.cron}).Validate()
			if err == nil {
				t.Fatal("invalid cron accepted")
			}
			for _, want := range []string{"cron", tc.want, `example: "0 * * * *"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want %q", err, want)
				}
			}
		})
	}
	cron := "0 * * * *"
	if err := (cronspec.Override{Cron: &cron}).Validate(); err != nil {
		t.Fatalf("suggested example refused: %v", err)
	}
}
