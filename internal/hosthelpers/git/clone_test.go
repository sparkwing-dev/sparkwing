package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneAndFetchUpstream(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "first.txt")
	dest := filepath.Join(root, "dest")
	if err := Clone(t.Context(), upstream, dest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "first.txt")); err != nil || string(got) != "first.txt\n" {
		t.Fatalf("cloned bytes = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(upstream, "second.txt"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, upstream, "add", "second.txt")
	runTestGit(t, upstream, "commit", "--quiet", "-m", "second")
	if err := Fetch(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dest, "show", "origin/main:second.txt")
	if got, err := cmd.Output(); err != nil || string(got) != "second" {
		t.Fatalf("fetched bytes = %q, %v", got, err)
	}
}

func TestCloneLeavesExistingDestination(t *testing.T) {
	root := t.TempDir()
	upstream := newTestRepo(t, filepath.Join(root, "upstream"), "first.txt")
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dest, "keep")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clone(t.Context(), upstream, dest); err == nil {
		t.Fatal("clone replaced existing destination")
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "mine" {
		t.Fatalf("existing bytes = %q, %v", got, err)
	}
}

func newTestRepo(t *testing.T, dir, file string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", file)
	runTestGit(t, dir, "commit", "--quiet", "-m", "seed")
	return dir
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func TestCloneAndFetchUseOriginDirectly(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "clone")
	command := exec.Command("git", "init", "--bare", source)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	t.Setenv("SPARKWING_GITCACHE_URL", "http://invalid.example")
	if err := Clone(t.Context(), source, dest); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(t.Context(), dest); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "keep")
	if err := os.WriteFile(marker, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clone(t.Context(), source, dest); err == nil {
		t.Fatal("clone replaced an existing destination")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "retained" {
		t.Fatalf("destination changed: %s %v", raw, err)
	}
}
