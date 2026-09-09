package inputs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		for _, resolve := range []sparkwing.CacheKeyFn{RepoFiles(), Files("sample.txt")} {
			key, err := resolve(t.Context())
			var pathError *os.PathError
			if key != "" || !errors.As(err, &pathError) || pathError.Path != expectedPath {
				t.Errorf("tracked directory = (%q, %v)", key, err)
			}
		}
	})
}

func TestRepoInputsPreserveGitDiagnostics(t *testing.T) {
	withWorkDir(t, t.TempDir(), func() {
		key, err := RepoFiles()(t.Context())
		if key != "" || err == nil || !strings.Contains(err.Error(), "rev-parse") || !strings.Contains(err.Error(), "fatal:") {
			t.Fatalf("repository lookup = (%q, %v)", key, err)
		}
	})
}

func TestComposePreservesFailureAndExplicitBypass(t *testing.T) {
	cause := errors.New("sample input failure")
	for _, fail := range []bool{false, true} {
		calls := 0
		first := func(context.Context) (sparkwing.CacheKey, error) {
			if fail {
				return "sample-key", cause
			}
			return sparkwing.NoCache, nil
		}
		second := func(context.Context) (sparkwing.CacheKey, error) {
			calls++
			return "sample-key", nil
		}
		key, err := Compose(first, second)(t.Context())
		if fail {
			if key != "" || !errors.Is(err, cause) {
				t.Errorf("composition failure = (%q, %v)", key, err)
			}
		} else if key != sparkwing.NoCache || err != nil {
			t.Errorf("composition bypass = (%q, %v)", key, err)
		}
		if calls != 0 {
			t.Errorf("later input calls = %d", calls)
		}
	}
}

func TestTreeHashStopsAfterCancellation(t *testing.T) {
	root := t.TempDir()
	writeAll(t, root, map[string]string{"sample.txt": "sample"})
	resolverContext, cancel := context.WithCancel(t.Context())
	cancel()
	hash, err := hashTree(resolverContext, root)
	if hash != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tree = (%q, %v)", hash, err)
	}
}
