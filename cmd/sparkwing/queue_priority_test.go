package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func queuePriorityOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runQueuePriority(args) })
	return out, err
}

func queuePriorityHolder(t *testing.T, home, runID string, cores float64) *wingdclient.Lease {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect daemon: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:     runID,
		Resources: wingwire.HostResources{Cores: cores},
	}, nil)
	if err != nil {
		t.Fatalf("acquire %s: %v", runID, err)
	}
	return lease
}

func TestParseQueuePriority_AcceptsIntegersAndRelativeWords(t *testing.T) {
	for _, tc := range []struct {
		set      string
		priority int
		mode     string
	}{
		{"7", 7, ""},
		{"-3", -3, ""},
		{"0", 0, ""},
		{"front", 0, "front"},
		{"back", 0, "back"},
	} {
		priority, mode, err := parseQueuePriority(tc.set)
		if err != nil {
			t.Fatalf("parseQueuePriority(%q): %v", tc.set, err)
		}
		if priority != tc.priority || mode != tc.mode {
			t.Errorf("parseQueuePriority(%q) = (%d, %q), want (%d, %q)", tc.set, priority, mode, tc.priority, tc.mode)
		}
	}
	if _, _, err := parseQueuePriority("highest"); err == nil {
		t.Error("parseQueuePriority accepted a word that is neither front nor back")
	}
	if _, _, err := parseQueuePriority(""); err == nil {
		t.Error("parseQueuePriority accepted an empty --set")
	}
}

func TestRunQueuePriority_HolderSaysOnlyItsLaterNodesMove(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	queuePriorityHolder(t, home, "holding-run", 1)

	out, err := queuePriorityOutput(t, "--home", home, "--run", "holding-run", "--set", "7", "-o", "pretty")
	if err != nil {
		t.Fatalf("queue priority: %v", err)
	}
	if !strings.Contains(out, "run holding-run: priority 0 -> 7") {
		t.Errorf("pretty output missing the rank change:\n%s", out)
	}
	if !strings.Contains(out, "already holds a lease") {
		t.Errorf("pretty output missing the holder caveat:\n%s", out)
	}
}

func TestRunQueuePriority_JSONCarriesTheAckFields(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	queuePriorityHolder(t, home, "json-run", 1)

	out, err := queuePriorityOutput(t, "--home", home, "--run", "json-run", "--set", "front", "-o", "json")
	if err != nil {
		t.Fatalf("queue priority: %v", err)
	}
	var got queuePriorityResult
	if uerr := json.Unmarshal([]byte(out), &got); uerr != nil {
		t.Fatalf("json invalid: %v\n%s", uerr, out)
	}
	if got.RunID != "json-run" || !got.Found || !got.Holding {
		t.Errorf("json = %+v, want a found, holding json-run", got)
	}
	if got.Priority != 1 {
		t.Errorf("json priority = %d, want 1: front of an empty queue", got.Priority)
	}
}

func TestRunQueuePriority_PlainIsOneTabSeparatedRecord(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)
	queuePriorityHolder(t, home, "plain-run", 1)

	out, err := queuePriorityOutput(t, "--home", home, "--run", "plain-run", "--set", "2", "-o", "plain")
	if err != nil {
		t.Fatalf("queue priority: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("plain output = %d lines, want 1:\n%s", len(lines), out)
	}
	fields := strings.Split(lines[0], "\t")
	if len(fields) != 8 || fields[0] != "priority" || fields[1] != "plain-run" || fields[3] != "2" {
		t.Errorf("plain record = %q", fields)
	}
}

func TestRunQueuePriority_UnknownRunExitsOneWithTheInvisibleCaveat(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)

	_, err := queuePriorityOutput(t, "--home", home, "--run", "ghost", "--set", "3", "-o", "pretty")
	if err == nil {
		t.Fatal("queue priority exited 0 for a run local admission does not know")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(err.Error(), "not in local admission") {
		t.Errorf("error does not say the run is not in local admission: %v", err)
	}
	if !strings.Contains(err.Error(), "consumer has not claimed") {
		t.Errorf("error does not carry the invisible-until-claimed caveat: %v", err)
	}
}

func TestRunQueuePriority_NoDaemonIsANotFoundRatherThanAnInfrastructureFault(t *testing.T) {
	home := queueHome(t)

	_, err := queuePriorityOutput(t, "--home", home, "--run", "ghost", "--set", "3", "-o", "pretty")
	if err == nil {
		t.Fatal("queue priority exited 0 with no daemon running")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1: with no daemon there is nothing queued", code)
	}
}

func TestRunQueuePriority_UnreachableDaemonExitsWithTheInfrastructureCode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its directory mode")
	}
	home := queueHome(t)
	blockQueueSocket(t, home)

	_, err := queuePriorityOutput(t, "--home", home, "--run", "any", "--set", "3", "-o", "pretty")
	if err == nil {
		t.Fatal("queue priority exited 0 against a daemon it could not reach")
	}
	if code := exitCodeFor(err); code != 4 {
		t.Errorf("exit code = %d, want 4 (infrastructure)", code)
	}
}

func TestRunQueuePriority_RejectsMissingFlagsAndPositionals(t *testing.T) {
	home := queueHome(t)
	if err := runQueuePriority([]string{"--home", home, "--set", "3"}); err == nil {
		t.Error("queue priority accepted a missing --run")
	}
	if err := runQueuePriority([]string{"--home", home, "--run", "r", "--set", "3", "extra"}); err == nil {
		t.Error("queue priority accepted a positional argument")
	}
}
