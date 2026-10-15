package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func FuzzExecutorFailureMask(f *testing.F) {
	f.Add("private", false)
	f.Add("\"\\\x00界\n", true)
	f.Fuzz(func(t *testing.T, value string, secretCode bool) {
		acceptance.CheckExecutorFailureMask(t, value, secretCode)
	})
}

func FuzzClaimedAwardFence(f *testing.F) {
	for field := range uint8(8) {
		f.Add(field, "adversarial")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		acceptance.CheckClaimedAwardFence(t, field, value)
	})
}
