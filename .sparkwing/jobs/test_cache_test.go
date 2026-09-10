package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestOrdinaryTestStepsReusePassingResults(t *testing.T) {
	for name, run := range map[string]func(context.Context) error{
		"gate": runTest,
		"test": (&Test{}).run,
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
				log := &testCacheLog{}
				ctx := context.WithValue(context.Background(), sparkwing.RuntimePlumbing.Keys.Logger, log)
				if err := run(ctx); err != nil {
					t.Fatalf("attempt %d: %v", attempt+1, err)
				}
				if attempt == 1 && !strings.Contains(log.output(), "(cached)") {
					t.Fatalf("unchanged tests ran again instead of using cached results:\n%s", log.output())
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

type testCacheLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *testCacheLog) Log(_, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, message)
}

func (l *testCacheLog) Emit(record sparkwing.LogRecord) {
	l.Log(record.Level, record.Msg)
}

func (l *testCacheLog) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
