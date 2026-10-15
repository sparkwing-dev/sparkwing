package acceptance_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func testGoSDKFilesetIgnoreReadFailure(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "with-inputs"
		if empty {
			name = "empty-context"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			command := exec.CommandContext(t.Context(), "git", "init", "--quiet", dir)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("initialize fixture: %v: %s", err, output)
			}
			if !empty {
				if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("input"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			view := sparkwing.NewGit(dir, "", "", "", "", "")
			if _, err := view.FilesetHash(t.Context()); err != nil {
				t.Fatalf("context without ignore rules: %v", err)
			}
			ignore := filepath.Join(dir, ".dockerignore")
			if err := os.Mkdir(ignore, 0o700); err != nil {
				t.Fatal(err)
			}
			key, err := view.FilesetHash(t.Context())
			var failure *os.PathError
			if key != "" || !errors.As(err, &failure) || failure.Path != ignore {
				t.Fatalf("unreadable ignore rules = (%q, %v), want no key and path error", key, err)
			}
		})
	}
}
