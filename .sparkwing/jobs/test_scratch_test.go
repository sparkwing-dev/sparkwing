package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestPipelineRemovesItsSuiteTemporaryRoot(t *testing.T) {
	assertTestPipelineScratchRemoved(t, false)
}

func TestTestPipelineRemovesItsSuiteTemporaryRootAfterFailure(t *testing.T) {
	assertTestPipelineScratchRemoved(t, true)
}

func assertTestPipelineScratchRemoved(t *testing.T, fail bool) {
	t.Helper()
	root := gateFixtureRepo(t)
	marker := filepath.Join(t.TempDir(), "tmpdir")
	t.Setenv("SPARKWING_TEST_TMPDIR_MARKER", marker)
	failure := ""
	if fail {
		failure = `t.Fatal("deliberate")`
	}
	writeGoFile(t, filepath.Join(root, "internal", "tmpdir_probe_test.go"), fmt.Sprintf(`package internal

import (
	"os"
	"testing"
)

func TestRecordTMPDIR(t *testing.T) {
	if err := os.WriteFile(os.Getenv("SPARKWING_TEST_TMPDIR_MARKER"), []byte(os.Getenv("TMPDIR")), 0o600); err != nil {
		t.Fatal(err)
	}
	%s
}
`, failure))
	gitAddAll(t, root)

	err := (&Test{}).run(context.Background())
	if fail && err == nil {
		t.Fatal("test pipeline accepted a failing suite")
	}
	if !fail && err != nil {
		t.Fatalf("test pipeline: %v", err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	testRoot := string(raw)
	if !strings.HasPrefix(filepath.Base(testRoot), "sparkwing-go-test-") {
		t.Fatalf("suite TMPDIR = %q, want pipeline-owned root", testRoot)
	}
	if _, err := os.Stat(testRoot); !os.IsNotExist(err) {
		t.Fatalf("suite temporary root survived test pipeline: %v", err)
	}
}
