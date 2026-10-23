package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTrackedFileReadErrorsReachCallers(t *testing.T) {
	repository := createTestRepository(t, map[string]string{"sample.txt": "sample"})
	path := filepath.Join(repository, "sample.txt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	expectedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	withWorkDir(t, repository, func() {
		for _, resolve := range []sparkwing.MemoInput{inputs.RepoFiles(), inputs.Files("sample.txt")} {
			key, err := resolveTestMemo(t, resolve)
			var pathError *os.PathError
			if key != "" || !errors.As(err, &pathError) || pathError.Path != expectedPath {
				t.Errorf("tracked directory = (%q, %v)", key, err)
			}
		}
	})
}

func TestRepoInputsPreserveGitDiagnostics(t *testing.T) {
	withWorkDir(t, t.TempDir(), func() {
		key, err := resolveTestMemo(t, inputs.RepoFiles())
		if key != "" || err == nil || !strings.Contains(err.Error(), "rev-parse") || !strings.Contains(err.Error(), "fatal:") {
			t.Fatalf("repository lookup = (%q, %v)", key, err)
		}
	})
}

func TestTreeHashStopsAfterCancellation(t *testing.T) {
	root := t.TempDir()
	writeAll(t, root, map[string]string{"sample.txt": "sample"})
	resolverContext, cancel := context.WithCancel(t.Context())
	cancel()
	hash, err := hashTree(resolverContext, root, ".")
	if hash != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tree = (%q, %v)", hash, err)
	}
}
