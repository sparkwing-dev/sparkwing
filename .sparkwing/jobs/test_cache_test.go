package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestOrdinaryTestStepsReusePassingResults(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.3s of real work; the fast class runs under -short")
	}
	for name, run := range map[string]func(context.Context) error{
		"gate": runTest,
		"test": (&Test{}).run(""),
	} {
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			temporaryRoot := t.TempDir()
			t.Setenv("TMPDIR", temporaryRoot)
			t.Setenv("FIXTURE_EXPECTED_TMPDIR", temporaryRoot)
			t.Setenv("GOTMPDIR", "")
			source := filepath.Join(root, "internal", "cache_test.go")
			writeGoFile(t, source, `package internal
import (
 "os"
 "testing"
)
func TestTemporaryFiles(t *testing.T) {
 if os.Getenv("TMPDIR") != os.Getenv("FIXTURE_EXPECTED_TMPDIR") { t.Fatal("caller temporary directory changed") }
 if os.Getenv("SPARKWING_HOME") == "" { t.Fatal("the step pinned no sparkwing home") }
 t.TempDir()
}
`)
			gitAddAll(t, root)
			for attempt := range 2 {
				host := newTestHost(t, sparkwingtest.Options{})
				ctx := host.Context(t.Context())
				if err := run(ctx); err != nil {
					t.Fatalf("attempt %d: %v", attempt+1, err)
				}
				var output strings.Builder
				for _, record := range host.Records() {
					output.WriteString(record.Msg)
				}
				if attempt == 1 && !strings.Contains(output.String(), "(cached)") {
					t.Fatalf("unchanged tests ran again:\n%s", output.String())
				}
			}
			writeGoFile(t, source, `package internal
import "testing"
func TestTemporaryFiles(t *testing.T) { t.Fatal("changed test must run") }
`)
			if err := run(newTestContext(t)); err == nil {
				t.Fatal("cached pass hid a changed failing test")
			}
		})
	}
}

// TestTestStepRerunsWhenTheCheckoutChangesOutsideTheModule changes only a file
// go test does not hash and requires the package that reads the pinned
// checkout state to run again rather than replay its pass.
func TestTestStepRerunsWhenTheCheckoutChangesOutsideTheModule(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: three real go test runs; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "state_test.go"), `package internal
import (
 "os"
 "testing"
)
func TestReadsCheckoutState(t *testing.T) { _ = os.Getenv("`+productTestRepoStateVar+`") }
`)
	gitCommitAll(t, root, "base")
	run := func() string {
		host := newTestHost(t, sparkwingtest.Options{})
		if err := runTest(host.Context(t.Context())); err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for _, record := range host.Records() {
			output.WriteString(record.Msg + "\n")
		}
		return output.String()
	}
	run()
	if out := run(); !strings.Contains(out, "(cached)") {
		t.Fatalf("an unchanged checkout did not replay:\n%s", out)
	}
	writeGoFile(t, filepath.Join(root, "outside.yaml"), "changed: true\n")
	if out := run(); strings.Contains(out, "(cached)") {
		t.Fatalf("a changed checkout replayed a cached pass:\n%s", out)
	}
	writeGoFile(t, filepath.Join(root, "outside.yaml"), "changed: again\n")
	if out := run(); strings.Contains(out, "(cached)") {
		t.Fatalf("an edited untracked file replayed a cached pass:\n%s", out)
	}
}
