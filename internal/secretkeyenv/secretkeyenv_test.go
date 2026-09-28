package secretkeyenv

import (
	"os"
	"slices"
	"testing"
)

func TestHoldTakesTheKeyOutOfTheEnvironmentAndEnvironHandsItOn(t *testing.T) {
	t.Cleanup(Forget)
	t.Setenv("SPARKWING_SECRETS_KEY", "current")
	t.Setenv("SPARKWING_SECRETS_PREVIOUS_KEY", "previous")
	if err := Hold(); err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		if v, set := os.LookupEnv(name); set {
			t.Errorf("%s = %q after Hold, want it unset so a step inherits nothing", name, v)
		}
	}
	if got, prev := Key(), PreviousKey(); got != "current" || prev != "previous" {
		t.Errorf("Key, PreviousKey = %q, %q; want the held keys", got, prev)
	}
	got := Environ([]string{"PATH=/bin", "SPARKWING_SECRETS_KEY=stale"})
	want := []string{"PATH=/bin", "SPARKWING_SECRETS_KEY=current", "SPARKWING_SECRETS_PREVIOUS_KEY=previous"}
	if !slices.Equal(got, want) {
		t.Errorf("Environ = %v, want %v", got, want)
	}
	if got := Without(got); !slices.Equal(got, []string{"PATH=/bin"}) {
		t.Errorf("Without = %v, want the key variables gone", got)
	}
}
