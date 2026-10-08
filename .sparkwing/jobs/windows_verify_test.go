package jobs

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestWindowsVerifyRefusesNonWindowsHosts(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		plan := sparkwing.NewPlan()
		err := (&WindowsVerify{}).planForPlatform(plan, "windows-verify", goos)
		if err == nil || !strings.Contains(err.Error(), "requires native Windows") || len(plan.Nodes()) != 0 {
			t.Fatalf("%s plan = %v, %d nodes", goos, err, len(plan.Nodes()))
		}
	}
}

func TestWindowsVerifyReservesFourCores(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (&WindowsVerify{}).planForPlatform(plan, "windows-verify", "windows"); err != nil {
		t.Fatal(err)
	}
	if hints := plan.ResourceHints(); hints == nil || hints.Cores != 4 {
		t.Fatalf("Windows reservation = %#v, want four cores", hints)
	}
	if len(plan.Nodes()) != 1 {
		t.Fatalf("Windows jobs = %d, want one sequential check job", len(plan.Nodes()))
	}
}

func TestWindowsVerifyKeepsRaceFailuresAndRunsLaterChecksInOrder(t *testing.T) {
	var ran []string
	check := func(id string, cause error) windowsVerifyCheck {
		return windowsVerifyCheck{id: id, run: func(context.Context) error { ran = append(ran, id); return cause }}
	}
	work := sparkwing.NewWork()
	addWindowsVerifyChecks(work, []windowsVerifyCheck{
		check("runtime-race", errors.New("go test -race: C compiler failed")),
		check("source-installer", errors.New("source installer failed")),
		check("release-installer", nil),
	})
	log := &windowsVerifyRecordLog{}
	ctx := context.WithValue(t.Context(), sparkwing.RuntimePlumbing.Keys.Logger, log)
	ctx = context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Node, "windows-verify")
	_, err := sparkwing.RunWork(ctx, work)
	if err == nil || !strings.Contains(err.Error(), "C compiler failed") {
		t.Fatalf("race failure was masked: %v", err)
	}
	if want := []string{"runtime-race", "source-installer", "release-installer"}; !slices.Equal(ran, want) {
		t.Fatalf("check order = %v, want %v", ran, want)
	}
	results := map[string]sparkwing.LogRecord{}
	for _, record := range log.snapshot() {
		if record.Event == sparkwing.EventStepEnd {
			results[record.Msg] = record
		}
	}
	for id, cause := range map[string]string{"runtime-race": "C compiler failed", "source-installer": "source installer failed"} {
		record := results[id]
		errorText, _ := record.Attrs["error"].(string)
		if record.Attrs["outcome"] != "failed" || !strings.Contains(errorText, cause) {
			t.Fatalf("%s did not retain its failure: %+v", id, record)
		}
	}
	if results["release-installer"].Attrs["outcome"] != "success" {
		t.Fatalf("last check result = %+v", results["release-installer"])
	}
}

func TestWindowsVerifySelectsNativeRegressionFixtures(t *testing.T) {
	selection := regexp.MustCompile(windowsVerifyRuntimeTests)
	for _, relative := range []string{
		"internal/runners/local/process_windows_test.go", "sparkwing/exec_windows_test.go", "internal/depcache/depcache_npm_test.go",
		"internal/wingd/client/query_absent_windows_test.go", "internal/orchestrator/run_handle_windows_test.go",
	} {
		source, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", filepath.FromSlash(relative)), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, declaration := range source.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(function.Name.Name, "Test") {
				continue
			}
			count++
			if !selection.MatchString(function.Name.Name) {
				t.Errorf("windows-verify omits native fixture %s in %s", function.Name.Name, relative)
			}
		}
		if count == 0 {
			t.Fatalf("%s contains no regression fixtures", relative)
		}
	}
}

func TestWindowsVerifyIsolatesSubcommandsAndPreservesFailures(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("Windows verification requires Bash:", err)
	}
	for _, name := range productTestUnset {
		t.Setenv(name, "ambient-binding")
	}
	parentHome := t.TempDir()
	t.Setenv(productTestHomeVar, parentHome)
	t.Setenv(devEnvDisableVar, "")
	var probe strings.Builder
	for _, name := range productTestUnset {
		fmt.Fprintf(&probe, `[ "${%s+x}" != x ] || exit 33; `, name)
	}
	probe.WriteString(`[ "$SPARKWING_DEV_ENV_DISABLE" = 1 ] || exit 34; `)
	probe.WriteString(`printf '%s' "$SPARKWING_HOME"; exit 17`)
	err := runWindowsVerifyIsolated(t.Context(), probe.String())
	if err == nil {
		t.Fatal("subcommand failure was masked")
	}
	var exit *sparkwing.ExecError
	if !errors.As(err, &exit) || exit.ExitCode != 17 {
		t.Fatalf("subcommand = %v; want exit 17 after all isolation assertions", err)
	}
	home := strings.TrimSpace(exit.Stdout)
	if home == "" || home == parentHome {
		t.Fatalf("child home = %q; want a fresh isolated home retained in command output", home)
	}
	if _, statErr := os.Stat(home); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("suite home survived its failed command: %v", statErr)
	}
}

type windowsVerifyRecordLog struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (log *windowsVerifyRecordLog) Log(_, _ string) {}

func (log *windowsVerifyRecordLog) Emit(record sparkwing.LogRecord) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.records = append(log.records, record)
}

func (log *windowsVerifyRecordLog) snapshot() []sparkwing.LogRecord {
	log.mu.Lock()
	defer log.mu.Unlock()
	return slices.Clone(log.records)
}
