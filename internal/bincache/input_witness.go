package bincache

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type witnessContextKey struct{}

// InputWitness is an invocation-scoped proof of unchanged compiler inputs.
// Its serialized form is meaningful only when supplied by the trusted installer.
type InputWitness struct {
	States      map[string]string `json:"states"`
	Directory   string            `json:"directory"`
	Environment []string          `json:"environment"`
	Tool        string            `json:"tool"`
	Settings    []byte            `json:"settings"`
	Disabled    bool              `json:"disabled"`
	now         func() time.Time
	fullTrees   map[string]bool
}

// WithInputWitness collects a proof alongside the normal cache-key computation.
func WithInputWitness(ctx context.Context) (context.Context, *InputWitness) {
	w := &InputWitness{States: map[string]string{}}
	return context.WithValue(ctx, witnessContextKey{}, w), w
}

func inputWitness(ctx context.Context) *InputWitness {
	w, _ := ctx.Value(witnessContextKey{}).(*InputWitness)
	return w
}

func (w *InputWitness) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *InputWitness) capture(path string, info os.FileInfo) bool {
	if w == nil || w.Disabled {
		return false
	}
	if len(w.States) >= 100000 {
		w.Disabled = true
		return false
	}
	state := ""
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			w.Disabled = true
			return false
		}
		target, err := os.Lstat(resolved)
		if err != nil || !target.Mode().IsRegular() || !w.capture(resolved, target) {
			w.Disabled = true
			return false
		}
		state = "link:" + resolved
	} else if info.IsDir() {
		state = directoryIdentity(info)
	} else if value, stable := fileIdentity(info, w.clock()); stable {
		state = value
	}
	if state == "" {
		w.Disabled = true
		return false
	}
	if previous, ok := w.States[path]; ok && previous != state {
		w.Disabled = true
		return false
	}
	w.States[path] = state
	return true
}

func (w *InputWitness) tree(path string) {
	if w == nil || w.Disabled {
		return
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		w.Disabled = true
		return
	}
	for parent := absolute; ; parent = filepath.Dir(parent) {
		if w.fullTrees[parent] {
			return
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		w.Disabled = true
		return
	}
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !w.capture(path, info) {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		w.Disabled = true
	}
	if !w.Disabled {
		if w.fullTrees == nil {
			w.fullTrees = map[string]bool{}
		}
		w.fullTrees[absolute] = true
	}
}

func (w *InputWitness) sourceTree(path string, hashAllFiles bool) {
	if w == nil || w.Disabled {
		return
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		w.Disabled = true
		return
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		w.Disabled = true
		return
	}
	directories := map[string]string{}
	files, _, err := hashable(absolute, allFiles, hashAllFiles, directories)
	if err != nil || len(directories) == 0 {
		w.Disabled = true
		return
	}
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !w.capture(path, info) {
			w.Disabled = true
			return
		}
	}
	for path, before := range directories {
		info, err := os.Lstat(path)
		if err != nil || before == "" || directoryIdentity(info) != before {
			w.Disabled = true
			return
		}
		if !w.capture(path, info) {
			return
		}
	}
}

// Unchanged checks collected source and tool identities without querying Go settings.
func (w *InputWitness) Unchanged(ctx context.Context) bool { return w.unchanged(ctx) }

func (w *InputWitness) file(path string) {
	if w == nil || w.Disabled || path == "" {
		return
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		w.Disabled = true
		return
	}
	if _, captured := w.States[absolute]; captured {
		return
	}
	if _, err := os.Lstat(absolute); os.IsNotExist(err) {
		if previous, ok := w.States[absolute]; ok && previous != "absent" {
			w.Disabled = true
			return
		}
		w.States[absolute] = "absent"
		return
	}
	info, err := os.Lstat(absolute)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		w.capture(absolute, info)
		return
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		w.Disabled = true
		return
	}
	info, err = os.Lstat(absolute)
	if err != nil {
		w.Disabled = true
		return
	}
	w.capture(absolute, info)
}

func (w *InputWitness) unchanged(ctx context.Context) bool {
	if w == nil || w.Disabled || len(w.States) == 0 {
		return false
	}
	for path, previous := range w.States {
		if ctx.Err() != nil {
			return false
		}
		info, err := os.Lstat(path)
		if previous == "absent" && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false
		}
		state := ""
		if strings.HasPrefix(previous, "link:") {
			if info.Mode()&os.ModeSymlink == 0 {
				return false
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || "link:"+resolved != previous {
				return false
			}
			continue
		}
		if info.IsDir() {
			state = directoryIdentity(info)
		} else if value, stable := fileIdentity(info, w.clock()); stable {
			state = value
		}
		if state == "" || state != previous {
			return false
		}
	}
	return true
}

// Valid checks Go settings and source identities without listing packages.
func (w *InputWitness) Valid(ctx context.Context) bool {
	if w == nil || w.Disabled || len(w.States) == 0 || w.Tool == "" || w.Directory == "" || len(w.Settings) == 0 {
		return false
	}
	tool, err := gotoolchain.Executable(w.Environment)
	if err != nil || tool != w.Tool {
		return false
	}
	command := procenv.ControlCommand(w.Environment, tool, "env", "-json")
	command.Dir = w.Directory
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if gotoolchain.Run(ctx, command) != nil {
		return false
	}
	var expected, actual map[string]string
	if json.Unmarshal(w.Settings, &expected) != nil || json.Unmarshal(stdout.Bytes(), &actual) != nil {
		return false
	}
	// safety: GOGCCFLAGS contains a per-command temporary path. Compare the key's
	// build settings and selection paths instead of Go's diagnostic-only fields.
	for _, names := range [][]string{buildEnvironmentNames, {"GOENV", "GOMOD", "GOWORK", "GOTOOLDIR"}} {
		for _, name := range names {
			if expected[name] != actual[name] {
				return false
			}
		}
	}
	return w.unchanged(ctx)
}
