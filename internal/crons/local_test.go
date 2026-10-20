package crons

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func localRegistrationFixture(t *testing.T) (*harness, controllerwire.LocalScheduleRegistration) {
	t.Helper()
	h := newHarness(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	h.svc.Side = "local"
	root := writeRepo(t, "local", twoSidedRepo)
	project, err := projectconfig.Load(filepath.Join(root, ".sparkwing", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []controllerwire.ScheduleDeclaration
	for _, p := range project.Registry().Pipelines {
		for _, e := range p.On.Schedule {
			if e.Where == "local" {
				entries = append(entries, controllerwire.ScheduleDeclaration{Pipeline: p.Name, Name: e.EffectiveName(), Cron: e.Cron, TZ: e.TZ, Overlap: e.Overlap, CatchUp: e.CatchUp, Args: e.Args})
			}
		}
	}
	return h, controllerwire.LocalScheduleRegistration{Mode: controllerwire.ScheduleReplace, RepoPath: root, Project: project, Schedules: entries, Environment: []string{"CAPTURE=install"}, Prove: true}
}

func localCapture(_ context.Context, environment []string) (runspec.EnvSnapshot, error) {
	values := map[string]string{}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return runspec.EnvSnapshot{}, errors.New("bad assignment")
		}
		values[name] = value
	}
	return runspec.EnvironmentDigest(values, strings.Repeat("5", 64))
}

func TestLocalRegistrationPersistsSelectedInputsAndPins(t *testing.T) {
	h, input := localRegistrationFixture(t)
	compiler := newFakeCompiler(t)
	report, err := h.svc.RegisterLocal(t.Context(), input, compiler.prove, localCapture, nil)
	if err != nil || len(report.Schedules) != 2 {
		t.Fatalf("register: %+v: %v", report, err)
	}
	first := report.Schedules[0]
	if first.LockedBinary == "" || first.LockedDigest == "" {
		t.Fatal("missing proof pin")
	}
	registration, err := ReadLocalRegistration(t.Context(), h.home, first.ID)
	if err != nil || registration.RepoPath != input.RepoPath || !reflect.DeepEqual(registration.Project, input.Project) {
		t.Fatalf("registration: %+v: %v", registration, err)
	}
	seal, _ := localCapture(t.Context(), input.Environment)
	if !reflect.DeepEqual(registration.Environment, seal) {
		t.Fatal("captured identity changed")
	}
	metadata, err := os.ReadFile(filepath.Join(h.home, "schedule-registrations", first.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "CAPTURE=install") {
		t.Fatal("environment values entered registration metadata")
	}
	input.Mode = controllerwire.ScheduleUpsert
	input.Schedules = input.Schedules[:1]
	input.Environment = []string{"CAPTURE=changed"}
	input.Follow, input.Prove = true, false
	report, err = h.svc.RegisterLocal(t.Context(), input, nil, localCapture, nil)
	if err != nil || len(report.Withdrawals) != 0 {
		t.Fatalf("selective registration: %+v: %v", report, err)
	}
	rows, err := h.svc.List(t.Context())
	if err != nil || len(rows) != 2 || !rows[1].Declared {
		t.Fatalf("unselected row withdrawn: %+v: %v", rows, err)
	}
	updated, err := h.svc.Schedules.GetCronSchedule(t.Context(), first.ID)
	if err != nil || updated.LockedBinary != "" {
		t.Fatalf("follow retained pin: %+v: %v", updated, err)
	}
	if _, err := h.svc.Lock(t.Context(), first.ID, compiler.prove); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Unlock(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	registration, err = ReadLocalRegistration(t.Context(), h.home, first.ID)
	expected, _ := localCapture(t.Context(), input.Environment)
	if err != nil || !reflect.DeepEqual(registration.Environment, expected) {
		t.Fatal("pin changes lost the captured identity")
	}
}

func TestLocalRegistrationFailedProofKeepsPriorInputs(t *testing.T) {
	h, input := localRegistrationFixture(t)
	compiler := newFakeCompiler(t)
	report, err := h.svc.RegisterLocal(t.Context(), input, compiler.prove, localCapture, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ReadLocalRegistration(t.Context(), h.home, report.Schedules[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	input.Environment = []string{"CAPTURE=changed"}
	compiler.failFor = input.Schedules[1].Pipeline
	if _, err := h.svc.RegisterLocal(t.Context(), input, compiler.prove, localCapture, nil); err == nil {
		t.Fatal("failed proof accepted")
	}
	after, err := ReadLocalRegistration(t.Context(), h.home, report.Schedules[0].ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed proof replaced captured inputs")
	}
}

func FuzzLocalRegistrationRejectsForgedSelection(f *testing.F) {
	f.Add("foreign", "0 1 * * *")
	f.Add("sweep", "0 2 * * *")
	f.Fuzz(func(t *testing.T, pipeline, cron string) {
		h, input := localRegistrationFixture(t)
		input.Mode = controllerwire.ScheduleUpsert
		input.Schedules = input.Schedules[:1]
		original := input.Schedules[0]
		input.Schedules[0].Pipeline, input.Schedules[0].Cron = pipeline, cron
		if pipeline == original.Pipeline && cron == original.Cron {
			return
		}
		captured := false
		_, err := h.svc.RegisterLocal(t.Context(), input, nil, func(context.Context, []string) (runspec.EnvSnapshot, error) {
			captured = true
			return runspec.EnvSnapshot{}, nil
		}, nil)
		if err == nil || captured {
			t.Fatal("forged selection reached host capture")
		}
		rows, err := h.svc.List(t.Context())
		if err != nil || len(rows) != 0 {
			t.Fatal("forged selection wrote schedules")
		}
	})
}

func FuzzLocalRegistrationIdentityCannotEscapeHome(f *testing.F) {
	f.Add("../../outside")
	f.Add("crn_0123456789ab/../outside")
	f.Fuzz(func(t *testing.T, id string) {
		if localScheduleID.MatchString(id) {
			return
		}
		if _, err := ReadLocalRegistration(t.Context(), t.TempDir(), id); err == nil {
			t.Fatal("noncanonical schedule identity accepted")
		}
	})
}

func TestLocalRegistrationAndPinRetainProofUntilCopyAndReleaseOnFailure(t *testing.T) {
	h, input := localRegistrationFixture(t)
	compiler := newFakeCompiler(t)
	released := 0
	var sources []string
	prove := func(ctx context.Context, root, pipeline string) (Proof, error) {
		proof, err := compiler.prove(ctx, root, pipeline)
		if err != nil {
			return proof, err
		}
		sources = append(sources, proof.Binary)
		proof.Release = func() error { released++; return os.Remove(proof.Binary) }
		return proof, nil
	}
	report, err := h.svc.RegisterLocal(t.Context(), input, prove, localCapture, nil)
	if err != nil || len(report.Schedules) != 2 || released != 2 {
		t.Fatalf("registration did not copy and release proofs: %+v %v releases=%d", report, err, released)
	}
	for _, row := range report.Schedules {
		if _, err := os.Stat(row.LockedBinary); err != nil {
			t.Fatal("proof released before pin copy:", err)
		}
	}
	for _, source := range sources {
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Fatal("proof source survived release:", err)
		}
	}
	if _, err := h.svc.Lock(t.Context(), report.Schedules[0].ID, prove); err != nil || released != 3 {
		t.Fatalf("pin proof lifecycle: %v releases=%d", err, released)
	}
	compiler.failFor = input.Schedules[1].Pipeline
	if _, err := h.svc.RegisterLocal(t.Context(), input, prove, localCapture, nil); err == nil || released != 4 {
		t.Fatalf("earlier proof survived later failure: %v releases=%d", err, released)
	}
}

func TestLocalProofCleanupFailuresReachArmingAndPinning(t *testing.T) {
	h, input := localRegistrationFixture(t)
	compiler := newFakeCompiler(t)
	want := errors.New("proof cleanup failed")
	prove := func(ctx context.Context, root, pipeline string) (Proof, error) {
		proof, err := compiler.prove(ctx, root, pipeline)
		if err == nil {
			proof.Release = func() error { return want }
		}
		return proof, err
	}
	report, err := h.svc.RegisterLocal(t.Context(), input, prove, localCapture, nil)
	if !errors.Is(err, want) || len(report.Schedules) != 2 {
		t.Fatalf("arm lost cleanup failure: %+v %v", report, err)
	}
	if _, err := h.svc.Lock(t.Context(), report.Schedules[0].ID, prove); !errors.Is(err, want) {
		t.Fatalf("pin lost cleanup failure: %v", err)
	}
	compiler.failFor = input.Schedules[1].Pipeline
	if _, err := h.svc.RegisterLocal(t.Context(), input, prove, localCapture, nil); !errors.Is(err, want) {
		t.Fatalf("failed proof lost prior cleanup failure: %v", err)
	}
}

func TestLocalRegistrationCommitFailureRestoresFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			h, input := localRegistrationFixture(t)
			compiler := newFakeCompiler(t)
			before := map[string][]byte{}
			if existing {
				report, err := h.svc.RegisterLocal(t.Context(), input, compiler.prove, localCapture, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range report.Schedules {
					for _, path := range []string{row.LockedBinary, filepath.Join(h.home, "schedule-registrations", row.ID+".json")} {
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						before[path] = data
					}
				}
			}
			compiler.revision++
			input.Environment = []string{"CAPTURE=changed"}
			refused := errors.New("registration transaction refused")
			_, err := h.svc.RegisterLocal(t.Context(), input, compiler.prove, localCapture, func(context.Context, []store.CronSchedule, LocalRegistration, bool) (ArmReport, error) {
				return ArmReport{}, refused
			})
			if !errors.Is(err, refused) {
				t.Fatalf("commit: %v", err)
			}
			for _, entry := range input.Schedules {
				id := ScheduleID(input.RepoPath, entry.Pipeline, entry.Name)
				for _, path := range []string{filepath.Join(h.svc.PinRoot, id, PinnedBinaryName), filepath.Join(h.home, "schedule-registrations", id+".json")} {
					got, err := os.ReadFile(path)
					if existing {
						if err != nil || !bytes.Equal(got, before[path]) {
							t.Fatalf("failed re-registration changed %s: %v", path, err)
						}
					} else if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed registration left %s: %v", path, err)
					}
				}
			}
		})
	}
}
