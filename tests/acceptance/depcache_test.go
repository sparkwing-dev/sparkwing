package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func FuzzDepcacheArchiveConfinement(f *testing.F) {
	for _, name := range []string{"safe", "../victim", "$OUTSIDE/victim", "a/../../../victim", "a/../victim", `a\..\..\victim`} {
		f.Add(name, "$OUTSIDE", false)
		f.Add(name, "$OUTSIDE", true)
		f.Add(name, "../../outside", true)
	}
	f.Fuzz(func(t *testing.T, name, link string, symlink bool) {
		acceptance.CheckDepcacheConfinement(t, name, link, symlink)
	})
}

func FuzzDepcacheCaptureConfinement(f *testing.F) {
	for _, spelling := range []string{"absolute", "relative", "chain", "relative-chain"} {
		f.Add(spelling, true)
		f.Add(spelling, false)
	}
	f.Fuzz(func(t *testing.T, spelling string, keyfile bool) {
		acceptance.CheckDepcacheCaptureConfinement(t, spelling, keyfile)
	})
}

func FuzzDepcacheUploadBinding(f *testing.F) {
	for field := range uint8(12) {
		f.Add(field, "foreign")
		f.Add(field, "")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		acceptance.CheckDepcacheUploadBinding(t, field, value)
	})
}
