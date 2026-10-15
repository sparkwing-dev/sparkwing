package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func FuzzMemoVerification(f *testing.F) {
	for target := range uint8(3) {
		for failure := range uint8(6) {
			f.Add(target, failure, "adversarial")
		}
	}
	f.Fuzz(func(t *testing.T, target, failure uint8, value string) {
		acceptance.CheckMemoVerification(t, target, failure, value)
	})
}

func FuzzMemoScope(f *testing.F) {
	for field := range uint8(12) {
		f.Add(field, "adversarial")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		acceptance.CheckMemoScope(t, field, value)
	})
}
