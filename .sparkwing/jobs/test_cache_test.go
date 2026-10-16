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
		"test": (&Test{}).run(testGoCommand(currentHost())),
	} {
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			temporaryRoot := t.TempDir()
			t.Setenv("TMPDIR", temporaryRoot)
			t.Setenv("SPARKWING_TEST_EXPECTED_TMPDIR", temporaryRoot)
			t.Setenv("GOTMPDIR", "")
			source := filepath.Join(root, "internal", "cache_test.go")
			writeGoFile(t, source, `package internal
import (
 "os"
 "testing"
)
func TestTemporaryFiles(t *testing.T) {
 if os.Getenv("TMPDIR") != os.Getenv("SPARKWING_TEST_EXPECTED_TMPDIR") { t.Fatal("caller temporary directory changed") }
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
			if err := run(context.Background()); err == nil {
				t.Fatal("cached pass hid a changed failing test")
			}
		})
	}
}
