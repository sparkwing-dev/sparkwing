//go:build !windows

package bincache

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"
)

func newTreeWitness(t *testing.T) (*InputWitness, string) {
	t.Helper()
	base := t.TempDir()
	for _, name := range []string{"source", "external", "overlay", "links", "replacement"} {
		if err := os.Mkdir(filepath.Join(base, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(base, "source")
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, w := WithInputWitness(t.Context())
	now := time.Now().Add(time.Hour)
	w.now = func() time.Time { return now }
	w.Directory, w.Environment = root, append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	var err error
	w.Tool, err = gotoolchain.Executable(w.Environment)
	if err != nil {
		t.Fatal(err)
	}
	command := procenv.ControlCommand(w.Environment, w.Tool, "env", "-json")
	command.Dir = root
	w.Settings, err = command.Output()
	if err != nil {
		t.Fatal(err)
	}
	w.tree(root)
	scopeWitness(t, w)
	if !w.Valid(t.Context()) {
		t.Fatal("unchanged source was rejected")
	}
	return w, path
}

func fixtureDirectory(path, name string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(path)), name)
}

func scopeWitness(t *testing.T, w *InputWitness) {
	t.Helper()
	base := filepath.Dir(w.Directory)
	for path := range w.States {
		relative, err := filepath.Rel(base, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		info, err := os.Lstat(path)
		if err == nil && info.IsDir() {
			delete(w.States, path)
		}
	}
	w.ancestors = nil
}

func TestInputWitnessRoundTrip(t *testing.T) {
	w, _ := newTreeWitness(t)
	body, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var restored InputWitness
	if err := json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	restored.now = w.now
	if !restored.Valid(t.Context()) {
		t.Fatal("serialized witness rejected unchanged source")
	}
}

func TestInputWitnessRejectsSourceChanges(t *testing.T) {
	for _, name := range []string{"restored mtime", "new source", "new embed", "deleted", "replaced inode"} {
		t.Run(name, func(t *testing.T) {
			w, path := newTreeWitness(t)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "restored mtime":
				if err := os.WriteFile(path, []byte("package mint\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				err = os.Chtimes(path, info.ModTime(), info.ModTime())
			case "new source":
				err = os.WriteFile(filepath.Join(filepath.Dir(path), "extra.go"), []byte("package main\n"), 0o600)
			case "new embed":
				err = os.WriteFile(filepath.Join(filepath.Dir(path), "asset.txt"), []byte("new embedded content"), 0o600)
			case "deleted":
				err = os.Remove(path)
			case "replaced inode":
				replacement := filepath.Join(fixtureDirectory(path, "replacement"), "replacement")
				if err := os.WriteFile(replacement, []byte("package main\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				err = os.Rename(replacement, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Valid(t.Context()) {
				t.Fatal("changed compiler inputs accepted")
			}
		})
	}
}

func TestInputWitnessRejectsSymlinkAndCancellation(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		w, _ := newTreeWitness(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if w.Valid(ctx) {
			t.Fatal("cancelled verification accepted")
		}
	})
}

func TestInputWitnessIncludesExternalOverlaySource(t *testing.T) {
	w, path := newTreeWitness(t)
	external := filepath.Join(fixtureDirectory(path, "external"), "replacement.go")
	if err := os.WriteFile(external, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{path: external}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(fixtureDirectory(path, "overlay"), "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	listing, err := json.Marshal(map[string]any{"Dir": filepath.Dir(path), "ImportPath": "test", "GoFiles": []string{filepath.Base(path)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiledInputDigestWithWitness(func() ([]byte, error) { return listing, nil }, filepath.Dir(path), "-overlay="+overlayPath, "off", w); err != nil {
		t.Fatal(err)
	}
	scopeWitness(t, w)
	if !w.Valid(t.Context()) {
		t.Fatal("unchanged overlay inputs rejected")
	}
	if err := os.WriteFile(external, []byte("package mint\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.Valid(t.Context()) {
		t.Fatal("changed external overlay source accepted")
	}
}

func TestInputWitnessUsesStableGoSettings(t *testing.T) {
	w, _ := newTreeWitness(t)
	var settings map[string]string
	if err := json.Unmarshal(w.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	settings["GOGCCFLAGS"] = "-ffile-prefix-map=/temporary/other-go-invocation"
	var err error
	w.Settings, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	scopeWitness(t, w)
	if !w.Valid(t.Context()) {
		t.Fatal("temporary compiler diagnostics invalidated unchanged inputs")
	}
	settings["GOFLAGS"] += " -tags=changed"
	w.Settings, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if w.Valid(t.Context()) {
		t.Fatal("changed compiler flags accepted")
	}
}

func TestInputWitnessFileSymlink(t *testing.T) {
	for _, mutation := range []string{"retarget", "target edit"} {
		t.Run(mutation, func(t *testing.T) {
			w, path := newTreeWitness(t)
			target := filepath.Join(fixtureDirectory(path, "external"), "target.go")
			if err := os.WriteFile(target, []byte("package main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(filepath.Dir(path), "linked.go")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			w.States = map[string]string{}
			w.ancestors = nil
			w.fullTrees = nil
			w.tree(filepath.Dir(path))
			scopeWitness(t, w)
			body, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			var restored InputWitness
			if err := json.Unmarshal(body, &restored); err != nil {
				t.Fatal(err)
			}
			restored.now = w.now
			if !restored.Valid(t.Context()) {
				t.Fatal("unchanged file symlink rejected after serialization")
			}
			if mutation == "retarget" {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(target, []byte("package mint\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if restored.Valid(t.Context()) {
				t.Fatal("changed symlink input accepted")
			}
		})
	}
}

func TestInputWitnessDeclinesUnsupportedSymlinks(t *testing.T) {
	for _, kind := range []string{"dangling", "directory"} {
		t.Run(kind, func(t *testing.T) {
			w, path := newTreeWitness(t)
			target := fixtureDirectory(path, "external")
			if kind == "dangling" {
				target = filepath.Join(target, "missing")
			}
			if err := os.Symlink(target, filepath.Join(filepath.Dir(path), "link")); err != nil {
				t.Fatal(err)
			}
			w.States = map[string]string{}
			w.ancestors = nil
			w.fullTrees = nil
			w.tree(filepath.Dir(path))
			if w.Valid(t.Context()) {
				t.Fatal("unsupported symlink input accepted")
			}
		})
	}
}

func TestInputWitnessCoversIgnoredEmbedDirectory(t *testing.T) {
	w, source := newTreeWitness(t)
	root := filepath.Dir(source)
	command := testgit.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize ignored-input fixture: %v: %s", err, output)
	}

	for name, body := range map[string]string{".gitignore": "assets/\n", "assets/first.txt": "embedded"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.States = map[string]string{}
	w.ancestors = nil
	w.fullTrees = nil
	if _, err := hashDirIntoCounted(io.Discard, root, allFiles, false, w); err != nil {
		t.Fatal(err)
	}
	listing, err := json.Marshal(map[string]any{"Dir": root, "ImportPath": "test", "GoFiles": []string{filepath.Base(source)}, "EmbedFiles": []string{"assets/first.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiledInputDigestWithWitness(func() ([]byte, error) { return listing, nil }, root, "", "off", w); err != nil {
		t.Fatal(err)
	}
	scopeWitness(t, w)
	if !w.Valid(t.Context()) {
		t.Fatal("unchanged ignored embed source rejected")
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "second.txt"), []byte("added embed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w.Valid(t.Context()) {
		t.Fatal("new file in ignored embed directory accepted")
	}
}

func TestInputWitnessRejectsRestoredSymlinkBindingBetweenSnapshots(t *testing.T) {
	w, path := newTreeWitness(t)
	link := filepath.Join(fixtureDirectory(path, "links"), "linked.go")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	w.States = map[string]string{}
	w.ancestors = nil
	if _, err := snapshotHashable([]string{link}, w); err != nil {
		t.Fatal(err)
	}
	scopeWitness(t, w)
	if !w.Valid(t.Context()) {
		t.Fatal("unchanged symlink snapshot rejected")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotHashable([]string{link}, w); err != nil {
		t.Fatal(err)
	}
	if w.Valid(t.Context()) {
		t.Fatal("recreated symlink accepted under earlier snapshot")
	}
}

func TestInputWitnessRejectsChainedSymlink(t *testing.T) {
	w, path := newTreeWitness(t)
	directory := fixtureDirectory(path, "links")
	intermediate := filepath.Join(directory, "intermediate")
	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, intermediate); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(intermediate, link); err != nil {
		t.Fatal(err)
	}
	w.file(link)
	if w.Valid(t.Context()) {
		t.Fatal("chained symlink target accepted")
	}
}

func TestInputWitnessRejectsRestoredExternalTargetAncestor(t *testing.T) {
	w, path := newTreeWitness(t)
	directory := filepath.Join(fixtureDirectory(path, "external"), "original")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixtureDirectory(path, "links"), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	w.States = map[string]string{}
	w.ancestors = nil
	w.file(link)
	scopeWitness(t, w)
	if !w.unchanged(t.Context()) {
		t.Fatal("unchanged direct symlink proof rejected")
	}
	moved := directory + "-moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, directory); err != nil {
		t.Fatal(err)
	}
	if w.unchanged(t.Context()) {
		t.Fatal("restored target ancestor accepted")
	}
}
