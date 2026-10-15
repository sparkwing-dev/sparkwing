package nodehost

import (
	"testing"
)

func FuzzExecutorFailureMask(f *testing.F) {
	f.Add("private", false)
	f.Add("\x8c", true)
	f.Add("\"\\\x00界\n", true)
	f.Fuzz(func(t *testing.T, value string, secretCode bool) {
		checkExecutorFailureMask(t, value, secretCode)
	})
}

func FuzzClaimedAwardFence(f *testing.F) {
	for field := range uint8(8) {
		f.Add(field, "adversarial")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		checkClaimedAwardFence(t, field, value)
	})
}
