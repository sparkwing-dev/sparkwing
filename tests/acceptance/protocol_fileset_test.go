package acceptance_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func testGoSDKFilesetPathNames(t *testing.T) {
	for _, test := range []struct {
		name, path string
		unixOnly   bool
	}{
		{"plain", "input.txt", false},
		{"space", "an input.txt", false},
		{"unicode", "caf\u00e9.txt", false},
		{"tab", "an\tinput.txt", true},
		{"newline", "an\ninput.txt", true},
		{"quote", "an\"input.txt", true},
		{"backslash", "an\\input.txt", true},
		{"leading-space", " input.txt", false},
		{"trailing-space", "input.txt ", true},
	} {
		if test.unixOnly && runtime.GOOS == "windows" {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			for _, tracked := range []bool{false, true} {
				name := "untracked"
				if tracked {
					name = "tracked"
				}
				t.Run(name, func(t *testing.T) {
					recordLeaf(t)
					host := sparkwingtest.New(sparkwingtest.Options{})
					t.Cleanup(func() { _ = host.Close() })
					ctx := host.Context(t.Context())
					dir := t.TempDir()
					command := testgit.CommandContext(t.Context(), "git", "init", "--quiet", dir)
					if output, err := command.CombinedOutput(); err != nil {
						t.Fatalf("initialize fixture: %v: %s", err, output)
					}
					path := filepath.Join(dir, test.path)
					if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
						t.Fatal(err)
					}
					if tracked {
						command = testgit.CommandContext(t.Context(), "git", "-C", dir, "add", "--", test.path)
						if output, err := command.CombinedOutput(); err != nil {
							t.Fatalf("stage fixture: %v: %s", err, output)
						}
					}
					view := sparkwing.NewGit(dir, "", "", "", "", "")
					first, err := view.FilesetHash(ctx)
					if err != nil || first == "" {
						t.Fatalf("first fingerprint = %q, %v", first, err)
					}
					if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
						t.Fatal(err)
					}
					second, err := view.FilesetHash(ctx)
					if err != nil || second == "" || second == first {
						t.Fatalf("changed input fingerprint = %q, %v; original = %q", second, err, first)
					}
				})
			}
		})
	}
}

func testGoSDKFilesetIgnoreReadFailure(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "with-inputs"
		if empty {
			name = "empty-context"
		}
		t.Run(name, func(t *testing.T) {
			recordLeaf(t)
			host := sparkwingtest.New(sparkwingtest.Options{})
			t.Cleanup(func() { _ = host.Close() })
			ctx := host.Context(t.Context())
			dir := t.TempDir()
			command := testgit.CommandContext(t.Context(), "git", "init", "--quiet", dir)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("initialize fixture: %v: %s", err, output)
			}
			if !empty {
				if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("input"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			view := sparkwing.NewGit(dir, "", "", "", "", "")
			if _, err := view.FilesetHash(ctx); err != nil {
				t.Fatalf("context without ignore rules: %v", err)
			}
			ignore := filepath.Join(dir, ".dockerignore")
			if err := os.Mkdir(ignore, 0o700); err != nil {
				t.Fatal(err)
			}
			key, err := view.FilesetHash(ctx)
			var failure *sdkserve.HelperError
			if key != "" || !errors.As(err, &failure) || !strings.Contains(failure.Message, ignore) {
				t.Fatalf("unreadable ignore rules = (%q, %v), want no key and host helper path error", key, err)
			}
		})
	}
}
