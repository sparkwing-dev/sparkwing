package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestCapturedEnvironmentSurvivesRepeatedReadsAndPreservesExactValues(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment []string
	}{
		{"nil", nil}, {"empty", []string{}}, {"empty_value_and_separators", []string{"B=", "A=one=two\nthree"}}, {"native_bytes", []string{"BYTES=" + string([]byte{0xff, 0xfe})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment := tc.environment
			directory := t.TempDir()
			snapshots, err := NewFileEnvironmentSnapshots(directory)
			if err != nil {
				t.Fatal(err)
			}
			seal, err := snapshots.Capture(t.Context(), environment)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				loaded, err := snapshots.Load(t.Context(), seal)
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded) != len(environment) {
					t.Fatalf("snapshot lost assignments: %v", loaded)
				}
				for _, original := range environment {
					found := false
					for _, entry := range loaded {
						found = found || entry == original
					}
					if !found {
						t.Fatal("snapshot changed an assignment")
					}
				}
				if len(loaded) > 0 {
					loaded[0] = "MUTATED=caller"
				}
			}
			files, err := snapshotFiles(directory)
			if err != nil || len(files) != 1 {
				t.Fatalf("snapshot leaked temporary files: %v %v", files, err)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(directory, files[0].Name()))
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("snapshot is not owner-only: %v %v", info, err)
				}
			}
		})
	}
}

func snapshotFiles(directory string) ([]os.DirEntry, error) {
	files, err := os.ReadDir(directory)
	return slices.DeleteFunc(files, func(file os.DirEntry) bool { return file.Name() == snapshotSaltKeyName }), err
}

func TestCapturedEnvironmentRefusesAmbiguousAssignmentsWithoutWriting(t *testing.T) {
	environments := [][]string{{"MISSING"}, {"=value"}, {"A=secret\x00value"}, {"A\x00=secret"}, {"A=first", "A=second"}}
	if runtime.GOOS == "windows" {
		environments = append(environments, []string{"Path=first", "PATH=second"})
	}
	for _, environment := range environments {
		directory := t.TempDir()
		snapshots, err := NewFileEnvironmentSnapshots(directory)
		if err != nil {
			t.Fatal(err)
		}
		seal, err := snapshots.Capture(t.Context(), environment)
		if err == nil || seal.Digest != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "second") {
			t.Fatalf("invalid capture: seal=%+v error=%v", seal, err)
		}
		files, err := snapshotFiles(directory)
		if err != nil || len(files) != 0 {
			t.Fatalf("invalid capture wrote files: %v %v", files, err)
		}
	}
}

func TestCapturedEnvironmentRejectsChangedMetadataAndPrivateFiles(t *testing.T) {
	for _, mode := range []string{"digest", "names", "order", "duplicate_names", "missing", "malformed", "null", "null_member", "changed_value", "duplicate_assignment", "permissions", "symlink", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			if runtime.GOOS == "windows" && (mode == "permissions" || mode == "symlink") {
				t.Skip("DACL and reparse-point refusals require the native Windows fixture")
			}
			directory := t.TempDir()
			snapshots, err := NewFileEnvironmentSnapshots(directory)
			if err != nil {
				t.Fatal(err)
			}
			seal, err := snapshots.Capture(t.Context(), []string{"A=private-value", "B="})
			if err != nil {
				t.Fatal(err)
			}
			path := snapshots.path(seal)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "digest":
				seal.Digest = "../../outside"
			case "names":
				seal.Names = []string{"OTHER", "B"}
			case "order":
				seal.Names = []string{"B", "A"}
			case "duplicate_names":
				seal.Names = []string{"A", "A"}
			case "missing":
				err = os.Remove(path)
			case "malformed":
				err = os.WriteFile(path, []byte("private-value"), 0o600)
			case "null":
				err = os.WriteFile(path, []byte("null"), 0o600)
			case "null_member":
				err = os.WriteFile(path, []byte("[null]"), 0o600)
			case "changed_value", "duplicate_assignment":
				entries := [][]byte{[]byte("A=changed"), []byte("B=")}
				if mode == "duplicate_assignment" {
					entries = append(entries, []byte("A=private-value"))
				}
				data, marshalErr := json.Marshal(entries)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(path, data, 0o600)
			case "permissions":
				err = os.Chmod(path, 0o644)
			case "symlink":
				if err = os.Rename(path, path+"-target"); err == nil {
					err = os.Symlink(path+"-target", path)
				}
			case "cancelled":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			environment, err := snapshots.Load(ctx, seal)
			if err == nil || environment != nil || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), string(seal.Digest)) {
				t.Fatalf("invalid snapshot: environment=%v error=%v", environment, err)
			}
			if mode == "missing" && !errors.Is(err, os.ErrNotExist) || mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("snapshot lost its failure cause:", err)
			}
		})
	}
}

type capturedLoaderFunc func(context.Context, runspec.EnvSnapshot) ([]string, error)

func (f capturedLoaderFunc) Load(ctx context.Context, seal runspec.EnvSnapshot) ([]string, error) {
	return f(ctx, seal)
}

func TestSDKEnvironmentRechecksLoadedSnapshotBeforeBuilding(t *testing.T) {
	seal, err := runspec.EnvironmentDigest(map[string]string{"CAPTURED": "original"}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"changed", "partial", "cancelled", "mutated_seal", "missing_identity", "in_memory_changed", "in_memory_original"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			built := false
			refused := errors.New("snapshot read failed")
			policy := SDKEnvironment{Base: procenv.Base{Local: true, Captured: []string{"CAPTURED=original"}}, Build: func(_ procenv.Kind, base procenv.Base, _ procenv.Runtime) (procenv.Env, error) {
				built = true
				return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: base.Captured}, procenv.Runtime{})
			}}
			policy.Snapshots = capturedLoaderFunc(func(_ context.Context, received runspec.EnvSnapshot) ([]string, error) {
				switch mode {
				case "partial":
					return []string{"CAPTURED=original"}, refused
				case "cancelled":
					cancel()
				case "mutated_seal":
					received.Names[0] = "OTHER"
					return []string{"OTHER=original"}, nil
				}
				return []string{"CAPTURED=changed"}, nil
			})
			expected := seal
			if strings.HasPrefix(mode, "in_memory_") {
				policy.Snapshots = nil
				if mode == "in_memory_changed" {
					policy.Base.Captured = []string{"CAPTURED=changed"}
				}
			}
			if mode == "missing_identity" {
				expected = runspec.EnvSnapshot{}
			}
			environment, err := policy.build(ctx, procenv.Plan, expected, procenv.Runtime{})
			if mode == "in_memory_original" {
				if err != nil || !built || !reflect.DeepEqual(slices.Collect(environment.Values()), []string{"CAPTURED=original"}) {
					t.Fatalf("verified in-memory capture: %v %v", environment, err)
				}
				return
			}
			if err == nil || len(slices.Collect(environment.Values())) != 0 || built || seal.Names[0] != "CAPTURED" {
				t.Fatalf("snapshot bypassed verification: env=%v built=%v error=%v", environment, built, err)
			}
			if mode == "partial" && !errors.Is(err, refused) || mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("snapshot lost its failure cause:", err)
			}
		})
	}
}

func TestClaimedNodeHostRefusesChangedCapturedValuesBeforeAcquisition(t *testing.T) {
	input, state, host, processes := claimedNodeFixture(t)
	state.run.Spec.Request.Request.Env, _ = runspec.EnvironmentDigest(map[string]string{"CAPTURED": "original"}, strings.Repeat("5", 64))
	input.Accepted.Spec, _ = json.Marshal(state.run.Spec)
	host.Source.Base = procenv.Base{Local: true, Captured: []string{"CAPTURED=changed"}}
	host.Acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) {
		t.Fatal("acquired source after the captured environment changed")
		return nil, nil
	}
	err := host.Execute(t.Context(), input)
	if err == nil || len(*processes) != 0 || len(state.changes) != 1 || state.changes[0].Cause != store.CauseReport || state.changes[0].To != store.NodeFailed {
		t.Fatalf("changed snapshot: changes=%v error=%v", state.changes, err)
	}
}

func TestOwnerHostRefusesChangedCapturedValuesBeforeSourceEnvironment(t *testing.T) {
	state, host, input := newOwnerFixture(t, false)
	request := &state.snapshot.Run.Request.Request
	request.Git.Repository.Key, request.Git.SHA = "git:example.com/acme/repo", strings.Repeat("a", 40)
	request.Source.Location = runspec.SourceLocation{RemoteURL: "https://example.com/acme/repo.git", PipelineDir: ".sparkwing"}
	request.Env, _ = runspec.EnvironmentDigest(map[string]string{"CAPTURED": "original"}, strings.Repeat("5", 64))
	input.SealedRequest, _ = json.Marshal(request)
	host.Acquire = nil
	host.Source.Base = procenv.Base{Local: true, Captured: []string{"CAPTURED=changed"}}
	host.Source.BuildEnvironment = func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error) {
		t.Fatal("built source environment after the captured values changed")
		return procenv.Env{}, nil
	}
	_, err := host.Run(t.Context(), input)
	if err == nil || state.finished.Transition.RunID == "" || state.accepted.RunID != "" {
		t.Fatalf("changed snapshot: finish=%v error=%v", state.finished, err)
	}
}

func TestCapturedEnvironmentSaltIsHostPrivateAndStable(t *testing.T) {
	environment := []string{"API_TOKEN=low-entropy", "PATH=/bin"}
	directory := t.TempDir()
	first, err := NewFileEnvironmentSnapshots(directory)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.Capture(t.Context(), environment)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileEnvironmentSnapshots(directory)
	if err != nil {
		t.Fatal(err)
	}
	b, err := reopened.Capture(t.Context(), environment)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("equal inputs on one host changed identity: %+v %+v %v", a, b, err)
	}
	other, err := NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := other.Capture(t.Context(), environment)
	if err != nil || c.Salt == a.Salt || c.Digest == a.Digest {
		t.Fatalf("another host shares the salted identity: %+v %+v %v", a, c, err)
	}
	for _, guess := range []string{strings.Repeat("0", 64), strings.Repeat("f", 64)} {
		if guessed, _ := runspec.EnvironmentDigest(map[string]string{"API_TOKEN": "low-entropy", "PATH": "/bin"}, guess); guessed.Digest == a.Digest {
			t.Fatal("a guessed salt reproduces the captured digest")
		}
	}
	if loaded, err := first.Load(t.Context(), a); err != nil || len(loaded) != 2 {
		t.Fatalf("load: %v %v", loaded, err)
	}
}
