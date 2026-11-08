package jobs

import (
	"fmt"
	"os"
	"testing"
)

// safety: a pipeline node hands its children the machine's sparkwing home, so
// the pre-release race step runs this binary against the operator's. The store
// guard refuses it, and a test that slipped past the guard would write the real
// runs store. Filesystem fixtures choose a test-scoped home, while this
// fallback protects helpers started without a filesystem fixture.
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

// TestCheckoutStateKeysTheTestCache reads the checkout state the gate pins, so
// it joins this package's test-cache key: go test does not hash the files above
// the module that these tests read. go test records a variable only while a
// test runs; TestMain's own reads, before m.Run, never reach the key.
func TestCheckoutStateKeysTheTestCache(t *testing.T) {
	_ = os.Getenv(productTestRepoStateVar)
}

func isolateTestHome(t *testing.T) {
	t.Helper()
	t.Setenv(productTestHomeVar, t.TempDir())
}

func TestFixtureHomesArePrivate(t *testing.T) {
	var previous string
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			gateFixtureRepo(t)
			home := os.Getenv(productTestHomeVar)
			if home == "" || home == previous {
				t.Fatalf("test reused home %q", home)
			}
			if previous != "" {
				if _, err := os.Stat(previous); !os.IsNotExist(err) {
					t.Fatalf("finished test's home survived: %v", err)
				}
			}
			previous = home
		})
	}
}
