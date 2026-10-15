package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func FuzzArtifactCaptureConfinement(f *testing.F) {
	for _, spelling := range []string{"absolute", "relative", "chain", "relative-chain"} {
		f.Add(spelling, true)
		f.Add(spelling, false)
	}
	f.Fuzz(func(t *testing.T, spelling string, literal bool) {
		acceptance.CheckArtifactCaptureConfinement(t, spelling, literal)
	})
}

func FuzzArtifactUploadBinding(f *testing.F) {
	for field := range uint8(18) {
		f.Add(field, "foreign")
		f.Add(field, "")
	}
	f.Add(uint8(3), "principal")
	f.Add(uint8(16), "result")
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		acceptance.CheckArtifactUploadBinding(t, field, value)
	})
}
