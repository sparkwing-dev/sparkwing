package jobs

import (
	"fmt"
	"os"
	"testing"
)

// safety: a pipeline node hands its children the machine's sparkwing home, so
// the pre-release race step runs this binary against the operator's. The store
// guard refuses it, and a test that slipped past the guard would write the real
// runs store. Nothing here needs that home; a test wanting its own still calls
// t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "sparkwing-jobs-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "jobs test: create sparkwing home:", err)
		os.Exit(1)
	}
	if err := os.Setenv("SPARKWING_HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "jobs test: set sparkwing home:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
