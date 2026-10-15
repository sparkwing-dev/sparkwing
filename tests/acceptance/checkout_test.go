package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func FuzzCheckoutGuardMask(f *testing.F) {
	f.Add("private")
	f.Add("\"\\\x00界\n")
	f.Add("\x7f")
	f.Fuzz(func(t *testing.T, value string) { acceptance.CheckCheckoutGuardMask(t, value) })
}
