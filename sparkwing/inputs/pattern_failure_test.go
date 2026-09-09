package inputs

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestMalformedPatternsFailWithoutTrackedFiles(t *testing.T) {
	repository := t.TempDir()
	command := exec.CommandContext(t.Context(), "git", "init", "--quiet", repository)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize repository: %v: %s", err, output)
	}
	withWorkDir(t, repository, func() {
		for _, pattern := range []string{"[", "[a-]"} {
			for _, resolve := range []sparkwing.CacheKeyFn{Files(pattern), RepoFiles(Ignore(pattern)), Files("*", pattern), RepoFiles(Ignore("*", pattern))} {
				key, err := resolve(t.Context())
				if key != "" || !errors.Is(err, filepath.ErrBadPattern) || !strings.Contains(err.Error(), "pattern") {
					t.Errorf("pattern %q = (%q, %v)", pattern, key, err)
				}
			}
		}
	})
}

func TestMalformedLaterPatternFailsBeforeMatching(t *testing.T) {
	repository := createTestRepository(t, map[string]string{"sample.txt": "sample"})
	withWorkDir(t, repository, func() {
		for _, resolve := range []sparkwing.CacheKeyFn{Files("*", "["), RepoFiles(Ignore("*", "["))} {
			key, err := resolve(t.Context())
			if key != "" || !errors.Is(err, filepath.ErrBadPattern) {
				t.Errorf("later malformed pattern = (%q, %v)", key, err)
			}
		}
	})
}

func TestPatternMatchingKeepsExistingSyntax(t *testing.T) {
	for _, testCase := range []struct {
		pattern, path string
		matched       bool
	}{
		{"[ab].txt", "nested/a.txt", true},
		{"[ab].txt", "nested/c.txt", false},
		{"sample/[ab].txt", "sample/[ab].txt", true},
		{"sample/[ab].txt", "sample/a.txt", false},
		{"[sample/", "[sample/note.txt", true},
		{"[sample/", "sample/note.txt", false},
	} {
		matcher, err := compilePattern(testCase.pattern)
		if err != nil {
			t.Fatal(err)
		}
		matched, err := matcher(testCase.path)
		if err != nil || matched != testCase.matched {
			t.Errorf("pattern %q path %q = (%t, %v), want %t", testCase.pattern, testCase.path, matched, err, testCase.matched)
		}
	}
}

func TestTrailingBackslashFollowsPlatformMatching(t *testing.T) {
	const pattern = "sample\\"
	matcher, err := compilePattern(pattern)
	if runtime.GOOS != "windows" {
		if !errors.Is(err, filepath.ErrBadPattern) || matcher != nil {
			t.Fatalf("trailing escape: matcher present=%t, error=%v", matcher != nil, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pattern, "sample", "nested\\sample"} {
		matched, err := matcher(path)
		if err != nil || matched {
			t.Errorf("trailing separator matched basename of %q: (%t, %v)", path, matched, err)
		}
	}
}
