package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func pauseFixture(t *testing.T) (config, credentials string) {
	t.Helper()
	dir := t.TempDir()
	credentials = filepath.Join(dir, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	config = filepath.Join(dir, "config.yaml")
	body := "runner:\n  controller: https://controller.example\n  holder_prefix: desk\n  credentials_dir: " + credentials + "\n"
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, credentials
}

func TestRunnersPauseAndResumeAreLocalAndIdempotent(t *testing.T) {
	config, credentials := pauseFixture(t)
	run := func(verb, format string) string {
		t.Helper()
		inv, err := clisurface.Parse("sparkwing", []string{"runners", verb, "--config", config, "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := setRunnerPaused(inv, verb == "pause", &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run("pause", "pretty"); !strings.Contains(got, "Paused runner desk on this machine") || !strings.Contains(got, "local to this machine") {
		t.Fatalf("pause output = %q", got)
	}
	if paused, err := runners.Paused(credentials); err != nil || !paused {
		t.Fatalf("pause left no marker: %v %v", paused, err)
	}
	var record struct {
		Runner          string
		Paused, Changed bool
	}
	if err := json.Unmarshal([]byte(run("pause", "json")), &record); err != nil || record.Runner != "desk" || !record.Paused || record.Changed {
		t.Fatalf("repeated pause = %+v %v", record, err)
	}
	if got := run("resume", "plain"); got != "desk\n" {
		t.Fatalf("resume plain = %q", got)
	}
	if paused, _ := runners.Paused(credentials); paused {
		t.Fatal("resume left the marker")
	}
	if got := run("resume", "pretty"); !strings.Contains(got, "is not paused") {
		t.Fatalf("repeated resume = %q", got)
	}
}

func TestRunnersPauseRefusesWithoutEnrolledRunner(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte("dashboard:\n  address: 127.0.0.1:0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := clisurface.Parse("sparkwing", []string{"runners", "pause", "--config", config})
	if err != nil {
		t.Fatal(err)
	}
	err = setRunnerPaused(inv, true, &bytes.Buffer{})
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "sparkwing runners add") {
		t.Fatalf("pause without a runner = %v", err)
	}
}

func TestRunnersListMarksOnlyTheLocallyPausedRunner(t *testing.T) {
	reader := runnerReaderFake(func() (controllerwire.Runners, error) {
		return controllerwire.Runners{Runners: []controllerwire.RunnerDetail{{Name: "desk", Status: "busy"}, {Name: "other", Status: "idle"}}}, nil
	})
	inv, err := clisurface.Parse("sparkwing", []string{"runners", "list", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listRunners(t.Context(), inv, reader, &out, "desk"); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var row controllerwire.RunnerDetail
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		statuses[row.Name] = row.Status
	}
	if statuses["desk"] != "paused" || statuses["other"] != "idle" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestLocallyPausedRunnerMatchesOnlyItsController(t *testing.T) {
	config, credentials := pauseFixture(t)
	t.Setenv("SPARKWING_CONFIG", config)
	if _, err := runners.SetPaused(credentials, true); err != nil {
		t.Fatal(err)
	}
	if got := locallyPausedRunner("https://controller.example/"); got != "desk" {
		t.Fatalf("paused runner for its controller = %q", got)
	}
	if got := locallyPausedRunner("https://other.example"); got != "" {
		t.Fatalf("paused runner reported for another controller: %q", got)
	}
}
