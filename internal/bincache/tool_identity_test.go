package bincache

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestToolIdentityMissingCandidatesNeedOneStatEach(t *testing.T) {
	dirs := []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two"), filepath.Join(t.TempDir(), "three")}
	env := []string{"PATH=" + dirs[0] + string(os.PathListSeparator) + dirs[1] + string(os.PathListSeparator) + dirs[2]}
	var stats, resolutions []string
	stat := func(path string) (os.FileInfo, error) {
		stats = append(stats, path)
		return nil, fs.ErrNotExist
	}
	resolve := func(path string) (string, error) {
		resolutions = append(resolutions, path)
		return "", fs.ErrNotExist
	}
	if got := toolIdentityWithFS(env, "missing.exe", stat, resolve); got != "absent" {
		t.Fatalf("missing tool identity = %q", got)
	}
	want := []string{filepath.Join(dirs[0], "missing.exe"), filepath.Join(dirs[1], "missing.exe"), filepath.Join(dirs[2], "missing.exe")}
	if !reflect.DeepEqual(stats, want) || len(resolutions) != 0 {
		t.Fatalf("missing candidates: stat %v, symlink walks %v; want one stat per candidate and no walks", stats, resolutions)
	}
}

func TestToolIdentityMatchesResolvedMetadata(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "compiler.exe")
	if err := os.WriteFile(target, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1234567890, 0)
	if err := os.Chtimes(target, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%s|4|%d", resolved, stamp.UnixNano())
	env := []string{"PATH=" + root}
	for _, tool := range []string{"compiler.exe", target} {
		if got := toolIdentity(env, tool); got != want {
			t.Errorf("identity of %s = %q, want %q", tool, got, want)
		}
	}
	if got := toolIdentity(env, "missing.exe"); got != "absent" {
		t.Errorf("missing identity = %q", got)
	}
	if got := toolIdentity(env, root); got != "absent" {
		t.Errorf("directory identity = %q", got)
	}
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "alias.exe")
		if err := os.Symlink("compiler.exe", link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := toolIdentity(env, "alias.exe"); got != want {
			t.Errorf("symlink identity = %q, want %q", got, want)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if got := toolIdentity(env, "alias.exe"); got != "absent" {
			t.Errorf("dangling identity = %q", got)
		}
	})
}

func TestBuildEnvironmentKeyFollowsCurrentToolIdentity(t *testing.T) {
	missingA, missingB := t.TempDir(), t.TempDir()
	env := func(path string) []string { return []string{"PATH=" + path} }
	absent := buildEnvironmentKey(env(missingA))
	if !bytes.Equal(absent, buildEnvironmentKey(env(missingB))) {
		t.Fatal("unrelated empty PATH directories changed the absent-tool key")
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	tool := filepath.Join(missingB, name)
	if err := os.WriteFile(tool, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	present := buildEnvironmentKey(env(missingB))
	if bytes.Equal(absent, present) {
		t.Fatal("a newly present compiler kept the absent-tool key")
	}
	if !bytes.Equal(present, buildEnvironmentKey(env(missingA+string(os.PathListSeparator)+missingB))) {
		t.Fatal("an earlier empty directory changed the present-tool key")
	}
	t.Run("symlink", func(t *testing.T) {
		aliases := t.TempDir()
		if err := os.Symlink(tool, filepath.Join(aliases, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if !bytes.Equal(present, buildEnvironmentKey(env(aliases))) {
			t.Fatal("a symlink to the same compiler changed the key")
		}
	})
	if err := os.WriteFile(filepath.Join(missingA, name), []byte("earlier compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(present, buildEnvironmentKey(env(missingA+string(os.PathListSeparator)+missingB))) {
		t.Fatal("a newly present higher-priority compiler reused the old key")
	}
	if err := os.WriteFile(tool, []byte("upgraded compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(present, buildEnvironmentKey(env(missingB))) {
		t.Fatal("a replaced compiler reused its old key")
	}
}

func TestReviewLongSymlinkChain(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux symlink traversal limit")
	}
	direct, aliases := t.TempDir(), t.TempDir()
	target := filepath.Join(direct, "go")
	if err := os.WriteFile(target, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := target
	for i := 0; i < 41; i++ {
		link := filepath.Join(aliases, fmt.Sprintf("link%d", i))
		if err := os.Symlink(next, link); err != nil {
			t.Fatal(err)
		}
		next = link
	}
	candidate := filepath.Join(aliases, "go")
	if err := os.Symlink(next, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidate); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("expected the native traversal limit, got %v", err)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%s|%d|%d", resolved, info.Size(), info.ModTime().UnixNano())
	env := []string{"PATH=" + aliases}
	for _, tool := range []string{"go", candidate} {
		if got := toolIdentity(env, tool); got != want {
			t.Errorf("identity of %s = %q, want %q", tool, got, want)
		}
	}
	if got, want := buildEnvironmentKey(env), buildEnvironmentKey([]string{"PATH=" + direct}); !bytes.Equal(got, want) {
		t.Errorf("cache key changed through the long symlink chain: direct=%x alias=%x", want, got)
	}
}

func TestToolIdentityOnlySkipsDefinitiveMissingCandidates(t *testing.T) {
	target := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(target, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		err     error
		missing bool
	}{
		{"not-exist", fs.ErrNotExist, true},
		{"not-directory", syscall.ENOTDIR, true},
		{"symlink-limit", syscall.ELOOP, false},
		{"permission", fs.ErrPermission, false},
		{"other", errors.New("transient stat error"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "tool.exe")
			var resolutions, stats int
			stat := func(path string) (os.FileInfo, error) {
				stats++
				if path == candidate {
					return nil, &os.PathError{Op: "stat", Path: path, Err: tc.err}
				}
				if path != target {
					t.Fatalf("unexpected stat %q", path)
				}
				return info, nil
			}
			resolve := func(path string) (string, error) {
				resolutions++
				if path != candidate {
					t.Fatalf("unexpected resolution %q", path)
				}
				return target, nil
			}
			got := toolIdentityWithFS(nil, candidate, stat, resolve)
			want := fmt.Sprintf("%s|%d|%d", target, info.Size(), info.ModTime().UnixNano())
			wantStats, wantResolutions := 2, 1
			if tc.missing {
				want, wantStats, wantResolutions = "absent", 1, 0
			}
			if got != want || stats != wantStats || resolutions != wantResolutions {
				t.Fatalf("identity=%q stats=%d resolutions=%d; want %q, %d, %d", got, stats, resolutions, want, wantStats, wantResolutions)
			}
		})
	}
}
