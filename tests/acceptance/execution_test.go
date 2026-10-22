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

func runCell(t *testing.T, name, driver string, check func(*testing.T)) {
	t.Helper()
	want := execution{Cell: t.Name() + "/" + name, Revision: revision(t), Platform: runtime.GOOS + "/" + runtime.GOARCH, Driver: driver, Result: "pass"}
	t.Run(name, func(t *testing.T) {
		t.Cleanup(func() {
			var result execution
			if !t.Failed() && !t.Skipped() {
				result = want
			}
			if err := result.validate(want); err != nil {
				t.Error(err)
				return
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Error(err)
				return
			}
			t.Log(string(raw))
		})
		check(t)
	})
}

func TestExecutionRequiresMatchingSuccessfulResult(t *testing.T) {
	want := execution{"cell", "source-sha256", "linux/amd64", "go-sdk", "pass"}
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
	for _, mode := range []string{"skip", "fail", "cleanup", "pass"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestExecutionChild$")
			command.Env = append(os.Environ(), "SPARKWING_EXECUTION_TEST="+mode, "SPARKWING_TESTLEAK_HOST=", "TMPDIR="+root, "TMP="+root, "TEMP="+root)
			output, err := command.CombinedOutput()
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("child left sandbox resources: %v, %v", entries, readErr)
			}
			if (err == nil) != (mode == "pass") {
				t.Fatalf("callback %s: %v\n%s", mode, err, output)
			}
			if (mode == "pass") != strings.Contains(string(output), `"result":"pass"`) {
				t.Fatalf("failed callback emitted pass: %s", output)
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
		if err := os.MkdirAll(os.Getenv("HOME"), 0700); err != nil {
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
