package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type doctorLifecycleFake struct {
	wingd.LocalLifecycle
	version  string
	calls    int
	deadline time.Time
	findings []wingd.LocalDoctorFinding
	err      error
}

func (f *doctorLifecycleFake) Doctor(ctx context.Context, version string) ([]wingd.LocalDoctorFinding, error) {
	f.calls++
	f.version = version
	f.deadline, _ = ctx.Deadline()
	return f.findings, f.err
}

func doctorTestDependencies(local *doctorLifecycleFake) doctorDependencies {
	return doctorDependencies{
		version: "v1.2.3",
		local:   func(context.Context) (dashboardServices, error) { return dashboardServices{lifecycle: local}, nil },
		machine: func(context.Context) doctorReport { return doctorReport{} },
	}
}

func TestDoctorCommandUsesControllerDiagnostics(t *testing.T) {
	findings := []wingd.LocalDoctorFinding{
		{Code: "removed_timer_verb", Unit: "nightly", Message: "timer calls crons tick", Hint: "sparkwing schedules install"},
		{Code: "version_skew", Unit: "controller", Message: "controller uses v1.0.0", Hint: "sparkwing update"},
		{Code: "incompatible_store", Message: "store schema is newer than this executable", Hint: "sparkwing update"},
	}
	for _, dry := range []bool{false, true} {
		local := &doctorLifecycleFake{findings: findings}
		args := []string{"--timeout", "2s", "-o", "json"}
		if dry {
			args = append(args, "--dry-run")
		}
		var out bytes.Buffer
		err := runDoctorCommand(t.Context(), args, &out, doctorTestDependencies(local))
		if err != nil {
			t.Fatal(err)
		}
		var got doctorReport
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if local.calls != 1 || local.version != "v1.2.3" || local.deadline.IsZero() || !sameDoctorFindings(got.Findings, findings) || got.DryRun != dry || got.ControllerError != "" {
			t.Fatalf("diagnostics lost: %+v local=%+v", got, local)
		}
	}
}

func TestDoctorCommandPreflightDoesNotResolveDependencies(t *testing.T) {
	for _, args := range [][]string{
		{"--timeout", "0s"}, {"--timeout", "-1s"}, {"--timeout", "nope"}, {"--timeout="},
		{"-o", "yaml"}, {"unexpected"}, {"--unknown"},
	} {
		var out bytes.Buffer
		err := runDoctorCommand(t.Context(), args, &out, doctorDependencies{})
		if err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("%v: %v %s", args, err, &out)
		}
	}
	var out bytes.Buffer
	if err := runDoctorCommand(t.Context(), []string{"--help", "-o", "json"}, &out, doctorDependencies{}); err != nil || !strings.Contains(out.String(), "sparkwing doctor") {
		t.Fatalf("help resolved dependencies: %v %s", err, &out)
	}
}

func TestDoctorCommandRetainsPartialFindingsAndFailure(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Unavailable, Message: "controller did not answer", Hint: "sparkwing daemon restart"}
	for _, resolveFailure := range []bool{false, true} {
		local := &doctorLifecycleFake{err: failure, findings: []wingd.LocalDoctorFinding{{Code: "removed_timer_verb", Unit: "nightly", Message: "timer calls crons tick"}}}
		deps := doctorTestDependencies(local)
		if resolveFailure {
			deps.local = func(context.Context) (dashboardServices, error) { return dashboardServices{}, failure }
		}
		var out bytes.Buffer
		err := runDoctorCommand(t.Context(), []string{"-o", "json"}, &out, deps)
		var got doctorReport
		if json.Unmarshal(out.Bytes(), &got) != nil || !errors.Is(err, failure) || got.ControllerError != failure.Message {
			t.Fatalf("partial failure lost: %+v %v %s", got, err, &out)
		}
		if !resolveFailure && !sameDoctorFindings(got.Findings, local.findings) {
			t.Fatalf("partial findings lost: %+v", got)
		}
	}
}

func TestDoctorCommandNilLifecycleFailsLoudly(t *testing.T) {
	deps := doctorTestDependencies(&doctorLifecycleFake{})
	deps.local = func(context.Context) (dashboardServices, error) { return dashboardServices{}, nil }
	var out bytes.Buffer
	err := runDoctorCommand(t.Context(), []string{"-o", "json"}, &out, deps)
	var structured *apierr.Error
	if !errors.As(err, &structured) || structured.Code != apierr.Unavailable || strings.Contains(out.String(), "healthy") {
		t.Fatalf("%v %s", err, &out)
	}
}

func TestDoctorCommandCancellationPreventsInspection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := runDoctorCommand(ctx, []string{"-o", "plain"}, &out, doctorDependencies{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestDoctorCommandMachineCancellationSkipsController(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deps := doctorDependencies{machine: func(context.Context) doctorReport {
		cancel()
		return doctorReport{GatesSurveyError: "registry could not be read"}
	}}
	var out bytes.Buffer
	err := runDoctorCommand(ctx, []string{"-o", "json"}, &out, deps)
	var got doctorReport
	if json.Unmarshal(out.Bytes(), &got) != nil || !errors.Is(err, context.Canceled) || got.ControllerChecked || got.GatesSurveyError != "registry could not be read" {
		t.Fatalf("machine result or cancellation lost: %+v %v", got, err)
	}
}

func TestDoctorCommandResolverCancellationSkipsLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	local := &doctorLifecycleFake{}
	deps := doctorTestDependencies(local)
	deps.local = func(context.Context) (dashboardServices, error) {
		cancel()
		return dashboardServices{lifecycle: local}, nil
	}
	var out bytes.Buffer
	err := runDoctorCommand(ctx, []string{"-o", "json"}, &out, deps)
	if !errors.Is(err, context.Canceled) || local.calls != 0 {
		t.Fatalf("cancellation reached lifecycle: %v %+v", err, local)
	}
}

func TestDoctorCommandRejectsIncompleteFindings(t *testing.T) {
	for _, finding := range []wingd.LocalDoctorFinding{{Message: "missing code"}, {Code: "missing_message"}} {
		local := &doctorLifecycleFake{findings: []wingd.LocalDoctorFinding{finding}}
		var out bytes.Buffer
		err := runDoctorCommand(t.Context(), []string{"-o", "json"}, &out, doctorTestDependencies(local))
		var got doctorReport
		var structured *apierr.Error
		if json.Unmarshal(out.Bytes(), &got) != nil || !errors.As(err, &structured) || structured.Code != apierr.ProtocolError || got.ControllerChecked || got.ControllerError == "" {
			t.Fatalf("incomplete finding accepted: %v %s", err, &out)
		}
	}
}

func TestDoctorCommandPreservesPrivateStateWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, "state.db")
	want := "controller-owned bytes, not a SQLite database"
	if err := os.WriteFile(state, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "absent-config"))
	for _, args := range [][]string{{"-o", "json"}, {"--dry-run", "-o", "json"}} {
		var out bytes.Buffer
		if err := runDoctorCommand(t.Context(), args, &out, doctorTestDependencies(&doctorLifecycleFake{})); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(state)
		if err != nil || string(got) != want {
			t.Fatalf("private state changed: %q %v", got, err)
		}
		entries, err := os.ReadDir(home)
		if err != nil || len(entries) != 1 {
			t.Fatalf("doctor created state: %v %v", entries, err)
		}
		if _, err := os.Stat(filepath.Join(root, "absent-config")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created config: %v", err)
		}
	}
}

func TestDoctorJSONUsesLowercaseFindingFields(t *testing.T) {
	local := &doctorLifecycleFake{findings: []wingd.LocalDoctorFinding{{Code: "version_skew", Unit: "controller", Message: "old executable", Hint: "sparkwing update"}}}
	var out bytes.Buffer
	if err := runDoctorCommand(t.Context(), []string{"-o", "json"}, &out, doctorTestDependencies(local)); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var findings []map[string]string
	if err := json.Unmarshal(got["findings"], &findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0]["code"] != "version_skew" || findings[0]["unit"] != "controller" || findings[0]["message"] != "old executable" || findings[0]["hint"] != "sparkwing update" {
		t.Fatalf("finding schema lost: %s", &out)
	}
}

func TestDoctorFormatsRetainActionableFindings(t *testing.T) {
	local := &doctorLifecycleFake{findings: []wingd.LocalDoctorFinding{{Code: "removed_timer_verb", Unit: "nightly", Message: "timer calls crons tick", Hint: "sparkwing schedules install"}}}
	for _, mode := range []string{"plain", "pretty"} {
		var out bytes.Buffer
		if err := runDoctorCommand(t.Context(), []string{"-o", mode}, &out, doctorTestDependencies(local)); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"removed_timer_verb", "nightly", "crons tick", "sparkwing schedules install"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s lost %s: %s", mode, want, &out)
			}
		}
	}
}

func TestDoctorCommandReturnsWriterAndControllerErrors(t *testing.T) {
	failure := errors.New("controller unavailable")
	local := &doctorLifecycleFake{err: failure}
	err := runDoctorCommand(t.Context(), []string{"-o", "json"}, doctorFailedWriter{}, doctorTestDependencies(local))
	if !errors.Is(err, io.ErrClosedPipe) || !errors.Is(err, failure) {
		t.Fatalf("errors lost: %v", err)
	}
}

type doctorFailedWriter struct{}

func (doctorFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func sameDoctorFindings(got []doctorFinding, want []wingd.LocalDoctorFinding) bool {
	if len(got) != len(want) {
		return false
	}
	for i, row := range got {
		if row.Code != want[i].Code || row.Unit != want[i].Unit || row.Message != want[i].Message || row.Hint != want[i].Hint {
			return false
		}
	}
	return true
}
