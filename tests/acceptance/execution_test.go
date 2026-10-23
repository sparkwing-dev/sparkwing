package acceptance_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

type execution struct {
	Cell     string `json:"cell"`
	Leaf     string `json:"leaf,omitempty"`
	Revision string `json:"revision"`
	Platform string `json:"platform"`
	Driver   string `json:"driver"`
	Result   string `json:"result"`
}

func (e execution) validate(want execution) error {
	if e.Cell == "" || e.Revision == "" || e.Platform == "" || e.Driver == "" || e.Result != "pass" {
		return fmt.Errorf("cell has no successful execution: %+v", e)
	}
	if e != want {
		return fmt.Errorf("execution does not match selected cell: got %+v, want %+v", e, want)
	}
	return nil
}

var sourceRevision struct {
	sync.Once
	value string
	err   error
}

func revision(t *testing.T) string {
	t.Helper()
	sourceRevision.Do(func() {
		// Hash worktree bytes too: HEAD alone cannot identify an uncommitted candidate.
		root, err := filepath.Abs("../..")
		if err != nil {
			sourceRevision.err = err
			return
		}
		cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		raw, err := cmd.Output()
		if err != nil {
			sourceRevision.err = err
			return
		}
		paths := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		sort.Strings(paths)
		hash := sha256.New()
		for _, path := range paths {
			fmt.Fprintf(hash, "%s\x00", path)
			body, err := os.ReadFile(filepath.Join(root, path))
			if os.IsNotExist(err) {
				hash.Write([]byte("deleted\x00"))
				continue
			}
			if err != nil {
				sourceRevision.err = err
				return
			}
			fmt.Fprintf(hash, "%d\x00", len(body))
			hash.Write(body)
		}
		sourceRevision.value = hex.EncodeToString(hash.Sum(nil))
	})
	if sourceRevision.err != nil {
		t.Fatal(sourceRevision.err)
	}
	return sourceRevision.value
}

type cellResults struct {
	mu     sync.Mutex
	leaves []execution
	want   execution
}

var activeCells sync.Map

// recordLeaf belongs at the start of an assertion-bearing leaf so its result
// includes every cleanup registered by the proof.
func recordLeaf(t *testing.T) {
	t.Helper()
	name := t.Name()
	for cell := name; cell != ""; {
		if value, ok := activeCells.Load(cell); ok {
			results := value.(*cellResults)
			t.Cleanup(func() {
				if t.Failed() || t.Skipped() {
					t.Error("assertion leaf did not pass")
					return
				}
				results.mu.Lock()
				result := results.want
				result.Leaf = name
				results.leaves = append(results.leaves, result)
				results.mu.Unlock()
			})
			return
		}
		index := strings.LastIndexByte(cell, '/')
		if index < 0 {
			break
		}
		cell = cell[:index]
	}
	t.Fatal("assertion leaf has no selected cell")
}

func runCell(t *testing.T, name, driver string, check func(*testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		want := execution{Cell: t.Name(), Revision: revision(t), Platform: runtime.GOOS + "/" + runtime.GOARCH, Driver: driver, Result: "pass"}
		results := &cellResults{want: want}
		activeCells.Store(t.Name(), results)
		t.Cleanup(func() {
			defer activeCells.Delete(t.Name())
			if t.Failed() || t.Skipped() || len(results.leaves) == 0 {
				t.Error("cell has no successful assertion leaf execution")
				return
			}
			for _, leaf := range results.leaves {
				leafWant := want
				leafWant.Leaf = leaf.Leaf
				if err := leaf.validate(leafWant); err != nil {
					t.Error(err)
					return
				}
			}
			for _, result := range append(results.leaves, want) {
				raw, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Log(string(raw))
			}
		})
		check(t)
	})
}

func TestExecutionRequiresMatchingSuccessfulResult(t *testing.T) {
	want := execution{Cell: "cell", Revision: "source-sha256", Platform: "linux/amd64", Driver: "go-sdk", Result: "pass"}
	if err := want.validate(want); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"missing", "cell", "revision", "platform", "driver", "skipped", "failed"} {
		t.Run(field, func(t *testing.T) {
			got := want
			switch field {
			case "missing":
				got = execution{}
			case "cell":
				got.Cell = "other"
			case "revision":
				got.Revision = "other"
			case "platform":
				got.Platform = "windows/arm64"
			case "driver":
				got.Driver = "mock"
			default:
				got.Result = field
			}
			if got.validate(want) == nil {
				t.Fatal("accepted execution without matching successful result")
			}
		})
	}
}

func TestExecutionRejectsSkippedFailedAndCleanupFailedCallbacks(t *testing.T) {
	for _, mode := range []string{"skip", "fail", "cleanup", "pass", "empty", "filtered", "nested", "nested-empty", "nested-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			selection := "^TestExecutionChild$"
			if mode == "filtered" {
				selection += "/^callback$/^absent$"
			}
			command := exec.Command(os.Args[0], "-test.v", "-test.run="+selection)
			command.Env = append(os.Environ(), "SPARKWING_EXECUTION_TEST="+mode, "SPARKWING_TESTLEAK_HOST=", "TMPDIR="+root, "TMP="+root, "TEMP="+root)
			output, err := command.CombinedOutput()
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("child left sandbox resources: %v, %v", entries, readErr)
			}
			passed := mode == "pass" || mode == "nested"
			if (err == nil) != passed {
				t.Fatalf("callback %s: %v\n%s", mode, err, output)
			}
			if passed != strings.Contains(string(output), `"result":"pass"`) {
				t.Fatalf("failed callback emitted pass: %s", output)
			}
			if passed && !strings.Contains(string(output), `"leaf":"TestExecutionChild/callback`) {
				t.Fatalf("cell passed without leaf evidence: %s", output)
			}
		})
	}
}

func TestExecutionChild(t *testing.T) {
	mode := os.Getenv("SPARKWING_EXECUTION_TEST")
	if mode == "" {
		return
	}
	runCell(t, "callback", "test", func(t *testing.T) {
		if mode == "empty" {
			return
		}
		if mode == "filtered" || mode == "nested" || mode == "nested-empty" || mode == "nested-cleanup" {
			t.Run("assertion", func(t *testing.T) {
				if mode == "nested-empty" {
					return
				}
				recordLeaf(t)
				if t.Name() == "" {
					t.Fatal("missing leaf identity")
				}
				if mode == "nested-cleanup" {
					t.Cleanup(func() { t.Error("leaf cleanup failed") })
				}
			})
			return
		}
		recordLeaf(t)
		if err := os.MkdirAll(os.Getenv("HOME"), 0o700); err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "skip":
			t.Skip("missing resource")
		case "fail":
			t.Fatal("failed assertion")
		case "cleanup":
			t.Cleanup(func() { t.Error("cleanup failed") })
		}
	})
}
