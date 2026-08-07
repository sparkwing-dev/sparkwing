package installsite_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

// fakeInstall writes an executable stand-in for a sparkwing binary in
// dir and returns its path.
func fakeInstall(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, installsite.ExeName())
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestScanCollapsesSymlinksToOneInstall guards the false positive that
// would make this feature unusable: /usr/local/bin/sparkwing linked at
// ~/.local/bin/sparkwing is one install with two names, and reporting it
// as a conflict would train operators to ignore the warning.
func TestScanCollapsesSymlinksToOneInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation on windows needs privileges this test will not assume")
	}
	dir := t.TempDir()
	real := fakeInstall(t, filepath.Join(dir, "real"))
	linkDir := filepath.Join(dir, "link")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, installsite.ExeName())
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	copies := installsite.Scan([]string{filepath.Dir(real), linkDir})
	if len(copies) != 1 {
		t.Fatalf("Scan found %d installs, want 1: %+v", len(copies), copies)
	}
	if len(installsite.Competing(copies, real)) != 0 {
		t.Fatalf("a symlink to the running install was reported as competing")
	}
}

func TestScanSkipsNonExecutableFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no execute bit to withhold")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, installsite.ExeName()), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if copies := installsite.Scan([]string{dir}); len(copies) != 0 {
		t.Fatalf("Scan picked up a non-executable file: %+v", copies)
	}
}

func TestCompetingExcludesOnlySelf(t *testing.T) {
	dir := t.TempDir()
	first := fakeInstall(t, filepath.Join(dir, "a"))
	second := fakeInstall(t, filepath.Join(dir, "b"))
	third := fakeInstall(t, filepath.Join(dir, "c"))

	competing := installsite.Competing(
		installsite.Scan([]string{filepath.Dir(first), filepath.Dir(second), filepath.Dir(third)}), second)
	if len(competing) != 2 {
		t.Fatalf("competing = %d, want 2: %+v", len(competing), competing)
	}
	for _, c := range competing {
		if c.Path == second {
			t.Fatalf("self was listed as competing")
		}
	}
}

// TestSearchDirsLooksBeyondTheCallersPath is the launchd half of the
// bug: the shell that runs the scan and the job that resolves the rival
// do not share a PATH, so a scan restricted to the caller's own PATH
// would report the conflicting machine as clean. The result must still
// be deduplicated -- GOPATH/bin and $HOME/go/bin are probed separately
// and are often the same directory.
func TestSearchDirsLooksBeyondTheCallersPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the well-known POSIX install dirs do not apply")
	}
	home := filepath.Join(t.TempDir(), "home")
	env := map[string]string{
		"PATH":   filepath.Join(home, "bin"),
		"GOPATH": filepath.Join(home, ".go"),
	}
	dirs := installsite.SearchDirs(func(k string) string { return env[k] }, home)

	for _, want := range []string{
		filepath.Join(home, "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".go", "bin"),
		"/usr/local/bin",
	} {
		if !slices.Contains(dirs, want) {
			t.Errorf("SearchDirs missing %q; got %v", want, dirs)
		}
	}
	seen := map[string]int{}
	for _, d := range dirs {
		seen[d]++
		if seen[d] > 1 {
			t.Fatalf("SearchDirs returned %q twice: %v", d, dirs)
		}
	}
}

// TestPathKeyIsStablePerInstall pins the property the per-path version
// stamps depend on: two names for one binary -- the scan resolves both
// to one path -- share a key, and two distinct installs never do.
func TestPathKeyIsStablePerInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation on windows needs privileges this test will not assume")
	}
	dir := t.TempDir()
	real := fakeInstall(t, filepath.Join(dir, "real"))
	other := fakeInstall(t, filepath.Join(dir, "other"))
	linkDir := filepath.Join(dir, "link")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, installsite.ExeName())
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	direct := installsite.Scan([]string{filepath.Dir(real)})
	viaLink := installsite.Scan([]string{linkDir})
	if len(direct) != 1 || len(viaLink) != 1 {
		t.Fatalf("scan setup broken: %+v / %+v", direct, viaLink)
	}
	if installsite.PathKey(direct[0].Resolved) != installsite.PathKey(viaLink[0].Resolved) {
		t.Fatalf("one binary reached by two names got two keys: %q vs %q",
			direct[0].Resolved, viaLink[0].Resolved)
	}

	rival := installsite.Scan([]string{filepath.Dir(other)})
	if installsite.PathKey(direct[0].Resolved) == installsite.PathKey(rival[0].Resolved) {
		t.Fatalf("two distinct installs share a key: %q vs %q",
			direct[0].Resolved, rival[0].Resolved)
	}
}
