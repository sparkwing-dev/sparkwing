package quicktest

import (
	"testing"
	"testing/quick"
)

func TestShortConfigChecksAFixedHandfulOfCases(t *testing.T) {
	full := &quick.Config{MaxCount: 500}
	got := Config(full)
	if !testing.Short() {
		if got != full {
			t.Fatal("the full suite must keep the property's own configuration")
		}
		return
	}
	if got.MaxCount != shortCount || got.Rand == nil {
		t.Fatalf("short config = %+v", got)
	}
	first, second := Config(full).Rand.Int63(), Config(full).Rand.Int63()
	if first != second {
		t.Fatal("short properties must draw the same cases every run")
	}
}
