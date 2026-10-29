//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// localSmokeGroups are the command groups served by the local controller. Every
// leaf in them is either driven below or named in localSmokeSkipped, so a new
// command cannot ship without meeting a real controller.
var localSmokeGroups = []string{"secrets", "tokens", "runners", "pools", "queue", "schedules", "runs"}

// localSmokeSkipped names the leaves the smoke test does not drive, with why.
var localSmokeSkipped = map[string]string{
	"sparkwing tokens create":        "a local peer mints only scope-less member tokens; covered by its own tests",
	"sparkwing tokens revoke":        "needs a token; covered by its own tests",
	"sparkwing tokens rotate":        "needs a token; covered by its own tests",
	"sparkwing runners add":          "enrolls a machine with a remote controller",
	"sparkwing runners remove":       "needs an enrolled runner",
	"sparkwing pools allow":          "needs a registered runner and repository",
	"sparkwing pools disallow":       "needs a registered runner and repository",
	"sparkwing queue priority":       "needs queued work",
	"sparkwing schedules install":    "writes the user's timer configuration",
	"sparkwing schedules uninstall":  "writes the user's timer configuration",
	"sparkwing schedules set":        "needs a repository checkout with schedules",
	"sparkwing schedules resume":     "the same route and authority as pause",
	"sparkwing runs grep":            "reads logs of finished runs; covered by its own tests",
	"sparkwing runs prune":           "deletes runs; covered by its own tests",
	"sparkwing runs retry":           "needs a failed run",
	"sparkwing runs annotations add": "needs a running job's credential",
	"sparkwing runs approvals deny":  "the same route and authority as approve",
}

// localSmokeStep is one command against the fresh local controller. A step
// with a want code must fail with exactly that code, which proves the request
// got past authorization to the resource it names.
type localSmokeStep struct {
	args  []string
	stdin string
	want  string
}

var localSmokeSteps = []localSmokeStep{
	{args: []string{"secrets", "set", "--secret", "SMOKE_VARIABLE", "--variable", "--value", "plain"}},
	{args: []string{"secrets", "set", "--secret", "SMOKE_SECRET"}, stdin: "masked-value"},
	{args: []string{"secrets", "list"}},
	{args: []string{"secrets", "get", "--secret", "SMOKE_VARIABLE"}},
	{args: []string{"secrets", "rotate"}},
	{args: []string{"secrets", "delete", "--secret", "SMOKE_SECRET"}},
	{args: []string{"tokens", "list"}},
	{args: []string{"runners", "list"}},
	{args: []string{"pools", "list"}},
	{args: []string{"queue", "list"}},
	{args: []string{"schedules", "list"}},
	{args: []string{"schedules", "show", "--schedule", "missing"}, want: "not_found"},
	{args: []string{"schedules", "pause", "--schedule", "missing"}, want: "not_found"},
	{args: []string{"schedules", "run", "--schedule", "missing"}, want: "not_found"},
	{args: []string{"runs", "list"}},
	{args: []string{"runs", "stats"}},
	{args: []string{"runs", "approvals", "list"}},
	{args: []string{"runs", "status", "--run", "missing"}, want: "not_found"},
	{args: []string{"runs", "logs", "--run", "missing"}, want: "not_found"},
	{args: []string{"runs", "cancel", "--run", "missing"}, want: "not_found"},
	{args: []string{"runs", "annotations", "list", "--run", "missing"}, want: "not_found"},
	{args: []string{"runs", "approvals", "approve", "--run", "missing", "--job", "job"}, want: "not_found"},
}

// TestLocalControllerSmoke drives every local controller command through the
// real CLI against a controller it starts in a fresh, isolated home: no profile,
// no fakes on either side of the wire.
func TestLocalControllerSmoke(t *testing.T) {
	driven := map[string]bool{}
	for _, step := range localSmokeSteps {
		path := "sparkwing"
		for _, word := range step.args {
			if strings.HasPrefix(word, "-") {
				break
			}
			path += " " + word
			driven[path] = true
		}
	}
	for _, command := range clisurface.Registry() {
		words := strings.Fields(command.Path)
		if len(words) < 2 || !slices.Contains(localSmokeGroups, words[1]) {
			continue
		}
		if _, skipped := localSmokeSkipped[command.Path]; !skipped && !driven[command.Path] {
			t.Errorf("%s: drive it in localSmokeSteps or name why not in localSmokeSkipped", command.Path)
		}
	}
	probe, err := net.Listen("unix", filepath.Join(t.TempDir(), "probe.sock"))
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("sandbox refuses local sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	cli := buildSubmitCLI(t)
	user := t.TempDir()
	home := filepath.Join(user, ".sparkwing")
	t.Setenv("HOME", user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, ".config"))
	t.Setenv("SPARKWING_CONFIG", filepath.Join(user, "missing.yaml"))
	t.Setenv("SPARKWING_HOME", home)
	offlineStopDaemon(t, home)
	work := t.TempDir()
	for _, step := range localSmokeSteps {
		name := strings.Join(step.args, " ")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		command := exec.CommandContext(ctx, cli, append(step.args, "-o", "json")...)
		command.Dir = work
		command.Stdin = strings.NewReader(step.stdin)
		var stderr strings.Builder
		command.Stderr = &stderr
		output, runErr := command.Output()
		cancel()
		code := localSmokeErrorCode(string(output) + stderr.String())
		switch {
		case step.want == "" && (runErr != nil || code != ""):
			t.Errorf("%s: %v (code %q)\n%s\n%s", name, runErr, code, output, &stderr)
		case step.want != "" && code != step.want:
			t.Errorf("%s: code %q, want %q\n%s\n%s", name, code, step.want, output, &stderr)
		}
	}
	if t.Failed() {
		if log, err := wingd.LogPath(home); err == nil {
			body, readErr := os.ReadFile(log)
			t.Logf("controller log: %s (%v)", body, readErr)
		}
	}
}

// localSmokeErrorCode returns the first error envelope's code in NDJSON output.
func localSmokeErrorCode(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		var envelope struct {
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &envelope) == nil && envelope.Error != nil {
			return envelope.Error.Code
		}
	}
	return ""
}
