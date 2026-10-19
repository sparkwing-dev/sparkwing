package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type updateUnitsFixture struct {
	wingd.ManagedUnitManager
	calls *[]string
	err   error
}

func (f updateUnitsFixture) Refresh(_ context.Context, executable, version string) error {
	*f.calls = append(*f.calls, "refresh "+executable+" "+version)
	return f.err
}

func updateCommandFixture(t *testing.T) (updateCommand, *bytes.Buffer, *[]string) {
	t.Helper()
	out, calls := &bytes.Buffer{}, []string{}
	path := filepath.Join(t.TempDir(), "sparkwing")
	command := updateCommand{
		out: out,
		check: func(version string, force, override bool) updateCheckReport {
			calls = append(calls, "check "+version)
			return updateCheckReport{Kind: "update_check", Tool: "sparkwing", Target: "cli", Strategy: "release", Status: "update_available"}
		},
		install: func(version string, force, override bool) (updateReceipt, error) {
			calls = append(calls, "install "+version)
			if !force || !override {
				t.Error("installation lost force/hold selection")
			}
			return updateReceipt{Kind: "update", Tool: "sparkwing", Target: "cli", Status: "updated", After: updateIdentity{Version: "v1.2.3", Path: path}, ResolvedRelease: "v1.2.3", Digest: strings.Repeat("a", 64)}, nil
		},
		units: func(context.Context) (wingd.ManagedUnitManager, error) {
			calls = append(calls, "units")
			return updateUnitsFixture{calls: &calls}, nil
		},
	}
	return command, out, &calls
}

func TestUpdateCommandRefreshesManagedUnitsAfterVerifiedInstall(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	if err := command.runArgs(t.Context(), []string{"--version", "v1.2.3", "--force", "--override-hold", "-o", "json"}); err != nil {
		t.Fatal(err)
	}
	var receipt updateReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	want := []string{"units", "install v1.2.3", "refresh " + receipt.After.Path + " v1.2.3"}
	if !reflect.DeepEqual(*calls, want) || receipt.ManagedUnits != "refreshed" {
		t.Fatalf("receipt=%+v calls=%v", receipt, *calls)
	}
}

func TestUpdateCommandSaysRunnerServicesUpdateThemselves(t *testing.T) {
	for _, status := range []string{"updated", "current"} {
		t.Run(status, func(t *testing.T) {
			command, out, _ := updateCommandFixture(t)
			install := command.install
			command.install = func(version string, force, override bool) (updateReceipt, error) {
				receipt, err := install(version, force, override)
				receipt.Status = status
				return receipt, err
			}
			if err := command.runArgs(t.Context(), []string{"--force", "--override-hold", "-o", "pretty"}); err != nil {
				t.Fatal(err)
			}
			lines := strings.Count(out.String(), updateRunnerServicesNote+"\n")
			if status == "updated" && lines != 1 || status == "current" && lines != 0 {
				t.Fatalf("%s receipt:\n%s", status, out)
			}
		})
	}
}

func TestUpdateCommandDryRunNeverInstallsOrTouchesUnits(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	err := command.runArgs(t.Context(), []string{"--dry-run", "--version", "v1.2.3", "-o", "json"})
	if exitCodeFor(err) != 1 || !reflect.DeepEqual(*calls, []string{"check v1.2.3"}) {
		t.Fatalf("err=%v calls=%v", err, *calls)
	}
	var report updateCheckReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != "update_available" {
		t.Fatalf("preview=%s err=%v", out, err)
	}
}

func TestUpdateCommandRejectsInvalidInputBeforeDependencies(t *testing.T) {
	for _, args := range [][]string{
		{"--cli"},
		{"--check"},
		{"--sdk"},
		{"--version="},
		{"--version", "../../tag"},
		{"--version", "v1.2"},
		{"--output", "jsonl"},
		{"--output="},
		{"extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command, out, calls := updateCommandFixture(t)
			if err := command.runArgs(t.Context(), args); err == nil || len(*calls) != 0 || out.Len() != 0 {
				t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
			}
		})
	}
}

func TestUpdateCommandNoReceiptOrRefreshAfterInstallFailure(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	failure := errors.New("release signature refused")
	command.install = func(string, bool, bool) (updateReceipt, error) {
		*calls = append(*calls, "install")
		return updateReceipt{}, failure
	}
	if err := command.runArgs(t.Context(), nil); !errors.Is(err, failure) || out.Len() != 0 || !reflect.DeepEqual(*calls, []string{"units", "install"}) {
		t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
	}
}

func TestUpdateCommandRequiresUnitIntegrationBeforeReplacement(t *testing.T) {
	for _, scenario := range []string{"error", "nil manager"} {
		t.Run(scenario, func(t *testing.T) {
			command, out, calls := updateCommandFixture(t)
			command.units = func(context.Context) (wingd.ManagedUnitManager, error) {
				if scenario == "nil manager" {
					return nil, nil
				}
				return nil, errors.New("unit manager unavailable")
			}
			if err := command.runArgs(t.Context(), nil); err == nil || len(*calls) != 0 || out.Len() != 0 {
				t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
			}
		})
	}
}

func TestUpdateCommandRefreshFailureNamesInstalledVersionWithoutSuccess(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	failure := errors.New("restart refused")
	command.units = func(context.Context) (wingd.ManagedUnitManager, error) {
		return updateUnitsFixture{calls: calls, err: failure}, nil
	}
	err := command.runArgs(t.Context(), []string{"--force", "--override-hold"})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "v1.2.3") || !strings.Contains(err.Error(), "installed") || out.Len() != 0 {
		t.Fatalf("err=%v output=%q", err, out)
	}
}

func TestUpdateCommandRefreshesCurrentInstallForRecovery(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	path := filepath.Join(t.TempDir(), "sparkwing")
	command.install = func(string, bool, bool) (updateReceipt, error) {
		return updateReceipt{Target: "cli", Status: "current", After: updateIdentity{Version: "v1.2.3", Path: path}}, nil
	}
	if err := command.runArgs(t.Context(), []string{"-o", "plain"}); err != nil || out.String() != "v1.2.3\n" || !reflect.DeepEqual(*calls, []string{"units", "refresh " + path + " v1.2.3"}) {
		t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
	}
}

func TestUpdateCommandHelpDoesNotResolveServices(t *testing.T) {
	command, out, calls := updateCommandFixture(t)
	if err := command.runArgs(t.Context(), []string{"--help", "-o", "json"}); err != nil || out.Len() == 0 || len(*calls) != 0 {
		t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
	}
}

func TestUpdateCommandPropagatesReceiptWriteFailure(t *testing.T) {
	command, _, _ := updateCommandFixture(t)
	command.out = updateFailWriter{}
	if err := command.runArgs(t.Context(), []string{"--force", "--override-hold", "-o", "pretty"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write failure=%v", err)
	}
}

func TestUpdateCommandRefusesUnconfirmedInstalledIdentity(t *testing.T) {
	for _, scenario := range []string{"target", "status", "path", "version", "digest", "release"} {
		t.Run(scenario, func(t *testing.T) {
			command, out, calls := updateCommandFixture(t)
			install := command.install
			command.install = func(version string, force, override bool) (updateReceipt, error) {
				result, err := install(version, force, override)
				switch scenario {
				case "target":
					result.Target = "sdk"
				case "status":
					result.Status = "unknown"
				case "path":
					result.After.Path = "relative"
				case "version":
					result.After.Version = ""
				case "digest":
					result.Digest = "invalid"
				case "release":
					result.ResolvedRelease = "v9.8.7"
				}
				return result, err
			}
			if err := command.runArgs(t.Context(), []string{"--force", "--override-hold"}); err == nil || out.Len() != 0 || !reflect.DeepEqual(*calls, []string{"units", "install "}) {
				t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
			}
		})
	}
}

func TestUpdateCommandDryRunStatusesAndWriterFailures(t *testing.T) {
	for _, scenario := range []struct {
		status string
		code   int
	}{
		{"current", 0}, {"ahead", 0}, {"update_available", 1}, {"unknown", 2},
	} {
		t.Run(scenario.status, func(t *testing.T) {
			command, out, calls := updateCommandFixture(t)
			command.check = func(version string, force, override bool) updateCheckReport {
				if version != "v1.2.3" || !force || !override {
					t.Fatalf("preview lost selection: %q %t %t", version, force, override)
				}
				return updateCheckReport{Status: scenario.status}
			}
			err := command.runArgs(t.Context(), []string{"--dry-run", "--version", "v1.2.3", "--force", "--override-hold", "-o", "plain"})
			if exitCodeFor(err) != scenario.code || out.String() != scenario.status+"\n" || len(*calls) != 0 {
				t.Fatalf("err=%v calls=%v output=%q", err, *calls, out)
			}
		})
	}
	command, _, _ := updateCommandFixture(t)
	command.out = updateFailWriter{}
	if err := command.runArgs(t.Context(), []string{"--dry-run", "-o", "pretty"}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("preview writer failure=%v", err)
	}
}

func TestUpdateCommandOutputDefaultsAndOverrides(t *testing.T) {
	for _, scenario := range []struct {
		name, mode string
		terminal   bool
	}{
		{"pipe", "", false}, {"terminal", "", true}, {"override terminal", "json", true}, {"override pipe", "pretty", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			command, out, _ := updateCommandFixture(t)
			command.terminal = scenario.terminal
			args := []string{"--force", "--override-hold"}
			if scenario.mode != "" {
				args = append(args, "--output="+scenario.mode)
			}
			if err := command.runArgs(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			jsonOutput := scenario.mode == "json" || scenario.mode == "" && !scenario.terminal
			if json.Valid(out.Bytes()) != jsonOutput || jsonOutput && strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("output mode=%q terminal=%t output=%q", scenario.mode, scenario.terminal, out)
			}
		})
	}
}

type updateFailWriter struct{}

func (updateFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
