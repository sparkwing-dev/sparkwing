package nodehost

import (
	"testing"
)

func FuzzMemoVerification(f *testing.F) {
	for target := range uint8(3) {
		for failure := range uint8(6) {
			f.Add(target, failure, "adversarial")
		}
	}
	f.Fuzz(func(t *testing.T, target, failure uint8, value string) {
		checkMemoVerification(t, target, failure, value)
	})
}

func FuzzMemoScope(f *testing.F) {
	for field := range uint8(12) {
		f.Add(field, "adversarial")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		checkMemoScope(t, field, value)
	})
}
