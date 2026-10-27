package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type queueManagerFake struct {
	controllerwire.QueueManager
	queue    wingwire.QueueState
	group    controllerwire.QueueGroup
	selected string
	calls    []string
	err      error
}

func (f *queueManagerFake) ReadQueue(context.Context) (wingwire.QueueState, error) {
	f.calls = append(f.calls, "queue")
	return f.queue, f.err
}

func (f *queueManagerFake) ReadQueueGroup(_ context.Context, group string) (controllerwire.QueueGroup, error) {
	f.calls = append(f.calls, "group")
	f.selected = group
	return f.group, f.err
}

func queueDependenciesFor(t *testing.T, manager *queueManagerFake, profile string) queueDependencies {
	t.Helper()
	return queueDependencies{connect: func(_ context.Context, got string) (controllerwire.QueueManager, func(), error) {
		if got != profile {
			t.Fatalf("profile %q, want %q", got, profile)
		}
		return manager, func() { manager.calls = append(manager.calls, "close") }, nil
	}}
}

func TestQueueCommandUsesOneControllerProjection(t *testing.T) {
	for _, profile := range []string{"", "prod"} {
		for _, format := range []string{"json", "plain", "pretty"} {
			manager := &queueManagerFake{queue: sampleQueueState()}
			args := []string{"list", "-o", format}
			if profile != "" {
				args = append(args, "--profile", profile)
			}
			var out bytes.Buffer
			if err := runQueueCommandArgs(t.Context(), args, &out, queueDependenciesFor(t, manager, profile)); err != nil {
				t.Fatal(err)
			}
			if strings.Join(manager.calls, ",") != "queue,close" || !strings.Contains(out.String(), "cores") {
				t.Fatalf("projection lost: calls=%v output=%s", manager.calls, &out)
			}
			if format == "json" {
				var got wingwire.QueueState
				if json.Unmarshal(out.Bytes(), &got) != nil || len(got.Resources) != len(manager.queue.Resources) || len(got.Waiters) != len(manager.queue.Waiters) {
					t.Fatalf("queue lost: %s", &out)
				}
			}
		}
	}
}

func TestQueueCommandPreflight(t *testing.T) {
	for _, args := range [][]string{
		{"list", "unexpected"},
		{"list", "-o", "yaml"},
		{"list", "--unknown"},
		{"list", "--group", ".."},
		{"priority", "--set", "3"},
		{"priority", "--run", "r1"},
		{"priority", "--run", "r1", "--set", "bad"},
		{"priority", "--run", "r1", "--set", "2", "extra"},
	} {
		var out bytes.Buffer
		err := runQueueCommandArgs(t.Context(), args, &out, queueDependencies{})
		if err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
			t.Fatalf("%v: err=%v output=%s", args, err, &out)
		}
	}
	for _, args := range [][]string{nil, {"list", "--help"}, {"priority", "--help", "-o", "json"}} {
		var out bytes.Buffer
		if err := runQueueCommandArgs(t.Context(), args, &out, queueDependencies{}); err != nil || out.Len() == 0 {
			t.Fatalf("help: %v %s", err, &out)
		}
	}
}

func TestQueueCommandKeepsFailureDistinctFromEmptyQueue(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Unavailable, Message: "controller cannot be reached"}
	for _, resolveFailure := range []bool{false, true} {
		manager := &queueManagerFake{err: failure}
		deps := queueDependenciesFor(t, manager, "")
		if resolveFailure {
			deps.connect = func(context.Context, string) (controllerwire.QueueManager, func(), error) { return nil, nil, failure }
		}
		var out bytes.Buffer
		err := runQueueCommandArgs(t.Context(), []string{"list", "-o", "json"}, &out, deps)
		if !errors.Is(err, failure) || out.Len() != 0 {
			t.Fatalf("unavailable queue printed success: %v %s", err, &out)
		}
	}
	manager := &queueManagerFake{}
	var out bytes.Buffer
	if err := runQueueCommandArgs(t.Context(), []string{"list", "-o", "json"}, &out, queueDependenciesFor(t, manager, "")); err != nil || out.Len() == 0 {
		t.Fatalf("empty queue: %v %s", err, &out)
	}
}

func TestQueueCommandPreservesGroupFacts(t *testing.T) {
	at := time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC)
	for _, format := range []string{"json", "plain", "pretty"} {
		manager := &queueManagerFake{group: controllerwire.QueueGroup{
			Group: "deploy", Capacity: 2, EffectiveCapacity: 1, UsedCost: 2,
			Holders: []controllerwire.QueueGroupHolder{{HolderID: "h1", RunID: "r1", JobID: "job1", Cost: 2, Superseded: true, ClaimedAt: at, LeaseExpiresAt: at.Add(time.Minute)}},
			Waiters: []controllerwire.QueueGroupWaiter{{RunID: "r2", JobID: "job2", Position: 1, Policy: "queue", Cost: 1, ArrivedAt: at}},
		}}
		var out bytes.Buffer
		err := runQueueCommandArgs(t.Context(), []string{"list", "--group", "deploy", "--profile", "prod", "-o", format}, &out, queueDependenciesFor(t, manager, "prod"))
		if err != nil || manager.selected != "deploy" || strings.Join(manager.calls, ",") != "group,close" {
			t.Fatalf("group: %v %+v", err, manager)
		}
		for _, want := range []string{"deploy", "r1", "job1", "r2", "job2", "2026-10-14T01:02:03Z", "2026-10-14T01:03:03Z"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s lost %q: %s", format, want, &out)
			}
		}
		if format == "json" {
			var got controllerwire.QueueGroup
			if json.Unmarshal(out.Bytes(), &got) != nil || got.EffectiveCapacity != 1 || !got.Holders[0].Superseded || got.Waiters[0].Position != 1 {
				t.Fatalf("group projection lost: %s", &out)
			}
		}
	}
}

func TestQueueGroupScope(t *testing.T) {
	for group, scope := range map[string]string{
		runspec.ConcurrencyKey("job", "global", "", "payments@db"): "global",
		runspec.ConcurrencyKey("job", "box", "host1", "db"):        "box (host1)",
		"memo:abc123": "unknown",
	} {
		var out bytes.Buffer
		if err := renderQueueGroup(&out, controllerwire.QueueGroup{Group: group}, "pretty"); err != nil || !strings.Contains(out.String(), scope) {
			t.Fatalf("scope for %s: %v %s", group, err, &out)
		}
	}
}

func TestQueueResolverCancellationSkipsObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	manager := &queueManagerFake{}
	deps := queueDependencies{connect: func(context.Context, string) (controllerwire.QueueManager, func(), error) {
		cancel()
		return manager, func() { manager.calls = append(manager.calls, "close") }, nil
	}}
	var out bytes.Buffer
	err := runQueueCommandArgs(ctx, []string{"list"}, &out, deps)
	if !errors.Is(err, context.Canceled) || strings.Join(manager.calls, ",") != "close" || out.Len() != 0 {
		t.Fatalf("canceled resolver reached observation: %v %v %s", err, manager.calls, &out)
	}
}

func TestQueuePriorityCommandKeepsExactAndRelativeValues(t *testing.T) {
	for _, value := range []string{"0", "-3", "7", "front", "back"} {
		var gotValue int
		var gotMode string
		deps := queueDependencies{priority: func(_ context.Context, run string, priority int, mode string) (queuePriorityResult, error) {
			gotValue, gotMode = priority, mode
			return queuePriorityResult{RunID: run, SetPriorityAck: wingwire.SetPriorityAck{Found: true, Priority: priority}}, nil
		}}
		var out bytes.Buffer
		if err := runQueueCommandArgs(t.Context(), []string{"priority", "--run", "r1", "--set", value, "-o", "json"}, &out, deps); err != nil {
			t.Fatalf("priority %s: %v", value, err)
		}
		switch value {
		case "front", "back":
			if gotMode != value || gotValue != 0 {
				t.Fatalf("relative mode lost: %d %q", gotValue, gotMode)
			}
		default:
			if gotMode != "" || strconv.Itoa(gotValue) != value {
				t.Fatalf("integer priority changed: %s %d %q", value, gotValue, gotMode)
			}
		}
	}
}

func TestQueuePriorityRefusesAGroup(t *testing.T) {
	if _, err := clisurface.Parse("sparkwing", []string{"queue", "priority", "--group", "deploy", "--run", "r1", "--set", "0"}); err == nil {
		t.Fatal("queue priority accepted --group")
	}
}

func TestQueueLocalPriorityKeepsHolderCaveatAndMissingResult(t *testing.T) {
	for _, found := range []bool{false, true} {
		deps := queueDependencies{priority: func(_ context.Context, run string, value int, mode string) (queuePriorityResult, error) {
			if run != "r1" || value != 0 || mode != "front" {
				t.Fatalf("local selection: %s %d %s", run, value, mode)
			}
			return queuePriorityResult{RunID: run, SetPriorityAck: wingwire.SetPriorityAck{Found: found, Holding: found, Priority: 1}}, nil
		}}
		var out bytes.Buffer
		err := runQueueCommandArgs(t.Context(), []string{"priority", "--run", "r1", "--set", "front", "-o", "pretty"}, &out, deps)
		if found && (err != nil || !strings.Contains(out.String(), "already holds a lease")) {
			t.Fatalf("holder: %v %s", err, &out)
		}
		if !found && (err == nil || exitCodeFor(err) != 1 || !strings.Contains(err.Error(), "not in local admission")) {
			t.Fatalf("missing: %v %s", err, &out)
		}
	}
}

func TestQueueCommandCancellationAndWriterErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := runQueueCommandArgs(ctx, []string{"list"}, &out, queueDependencies{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	for _, args := range [][]string{{"list", "-o", "plain"}, {"list", "--group", "deploy", "-o", "pretty"}} {
		manager := &queueManagerFake{queue: sampleQueueState(), group: controllerwire.QueueGroup{Group: "deploy"}}
		err := runQueueCommandArgs(t.Context(), args, doctorFailedWriter{}, queueDependenciesFor(t, manager, ""))
		if !errors.Is(err, io.ErrClosedPipe) || manager.calls[len(manager.calls)-1] != "close" {
			t.Fatalf("%v: writer/cleanup lost: %v %v", args, err, manager.calls)
		}
	}
}

func TestQueueRemoteRunPriorityRequiresAuthorizedRoute(t *testing.T) {
	var out bytes.Buffer
	err := runQueueCommandArgs(t.Context(), []string{"priority", "--profile", "prod", "--run", "r1", "--set", "3"}, &out, queueDependencies{})
	var structured *apierr.Error
	if !errors.As(err, &structured) || structured.Code != apierr.PolicyMissing || out.Len() != 0 {
		t.Fatalf("remote priority guessed a route: %v %s", err, &out)
	}
}

func TestQueueLocalPriorityResolvesRunNumbersBeforeMutation(t *testing.T) {
	for _, outcome := range []string{"resolved", "failed", "empty"} {
		failure := errors.New("run lookup failed")
		var calls []string
		deps := queueDependencies{
			resolveRun: func(_ context.Context, selector string) (string, error) {
				calls = append(calls, "resolve")
				if selector != "412" {
					t.Fatalf("number lost: %s", selector)
				}
				if outcome == "failed" {
					return "", failure
				}
				if outcome == "empty" {
					return "", nil
				}
				return "canonical-run", nil
			},
			priority: func(_ context.Context, run string, priority int, mode string) (queuePriorityResult, error) {
				calls = append(calls, "mutate")
				if run != "canonical-run" || priority != 0 || mode != "" {
					t.Fatalf("canonical priority: %s %d %s", run, priority, mode)
				}
				return queuePriorityResult{RunID: run, SetPriorityAck: wingwire.SetPriorityAck{Found: true}}, nil
			},
		}
		var out bytes.Buffer
		err := runQueueCommandArgs(t.Context(), []string{"priority", "--run", "412", "--set", "0", "-o", "json"}, &out, deps)
		if outcome == "failed" {
			if !errors.Is(err, failure) || out.Len() != 0 || strings.Join(calls, ",") != "resolve" {
				t.Fatalf("failed lookup mutated: %v %v %s", calls, err, &out)
			}
		} else if outcome == "empty" {
			var structured *apierr.Error
			if !errors.As(err, &structured) || structured.Code != apierr.ProtocolError || strings.Join(calls, ",") != "resolve" || out.Len() != 0 {
				t.Fatalf("empty identity mutated: %v %v %s", calls, err, &out)
			}
		} else if err != nil || strings.Join(calls, ",") != "resolve,mutate" || !strings.Contains(out.String(), "canonical-run") {
			t.Fatalf("number resolution: %v %v %s", calls, err, &out)
		}
	}
}
