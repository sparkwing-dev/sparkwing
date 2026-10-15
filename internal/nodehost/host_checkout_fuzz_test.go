package nodehost

import (
	"testing"
)

func FuzzCheckoutGuardMask(f *testing.F) {
	f.Add("private")
	f.Add("\"\\\x00界\n")
	f.Add("\x7f")
	f.Fuzz(func(t *testing.T, value string) { checkCheckoutGuardMask(t, value) })
}
