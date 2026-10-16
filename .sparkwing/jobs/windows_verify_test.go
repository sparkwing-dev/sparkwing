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
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestWindowsVerifyDeclaresPlacementOnAnyPlanningHost(t *testing.T) {
	plan := inspectPipeline(t, "windows-verify", nil)
	if len(plan.Jobs) != 1 || !slices.Equal(plan.Jobs[0].Requires, []string{"os=windows"}) {
		t.Fatalf("Windows placement = %+v", plan.Jobs)
	}
}

func TestWindowsVerifyReservesFourCores(t *testing.T) {
	plan := inspectPipeline(t, "windows-verify", nil)
	if hints := plan.Resources; hints == nil || hints.Cores != 4 {
		t.Fatalf("Windows reservation = %#v, want four cores", hints)
	}
	if len(plan.Jobs) != 1 {
		t.Fatalf("Windows jobs = %d, want one sequential check job", len(plan.Jobs))
	}
}

func TestWindowsVerifyKeepsRaceFailuresAndRunsLaterChecksInOrder(t *testing.T) {
	var ran []string
	check := func(id string, cause error) windowsVerifyCheck {
		return windowsVerifyCheck{id: id, run: func(context.Context) error { ran = append(ran, id); return cause }}
	}
	host, plan, doc := prepareWork(t, testJob(func(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
		addWindowsVerifyChecks(work, []windowsVerifyCheck{
			check("runtime-race", errors.New("C compiler failed")),
			check("source-installer", errors.New("source installer failed")),
			check("release-installer", nil),
		})
		return nil, nil
	}))
	for i, step := range doc.Steps {
		var wantDeps []string
		if i > 0 {
			wantDeps = []string{doc.Steps[i-1].ID}
		}
		if !step.ContinueOnError || !slices.Equal(step.Needs, wantDeps) {
			t.Fatalf("check declarations = %+v", step)
		}
		_, err := host.RunStep(t.Context(), plan, "job", step.ID)
		if i < 2 && err == nil {
			t.Fatalf("%s failure was masked", step.ID)
		}
		if i == 0 && !strings.Contains(err.Error(), "C compiler failed") {
			t.Fatalf("race failure = %v", err)
		}
		if i == 2 && err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"runtime-race", "source-installer", "release-installer"}; !slices.Equal(ran, want) {
		t.Fatalf("check order = %v, want %v", ran, want)
	}
}

func TestWindowsVerifySelectsNativeRegressionFixtures(t *testing.T) {
	selection := regexp.MustCompile(windowsVerifyRuntimeTests)
	for _, relative := range []string{
		"sparkwing/exec_windows_test.go", "internal/depcache/depcache_npm_test.go",
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
	var probe strings.Builder
	for _, name := range productTestUnset {
		fmt.Fprintf(&probe, `[ "${%s+x}" != x ] || exit 33; `, name)
	}
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
