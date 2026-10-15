package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type operatorManagerFake struct {
	controllerwire.OperatorManager
	limits controllerwire.OperatorLimits
	state  controllerwire.ObjectStoreBreaker
	write  controllerwire.OperatorLimitsWrite
	key    string
	calls  []string
	err    error
}

func (f *operatorManagerFake) ReadOperatorLimits(context.Context) (controllerwire.OperatorLimits, error) {
	f.calls = append(f.calls, "limits")
	return f.limits, f.err
}

func (f *operatorManagerFake) WriteOperatorLimits(_ context.Context, input controllerwire.OperatorLimitsWrite) (controllerwire.OperatorLimits, error) {
	f.calls = append(f.calls, "write")
	f.write = input
	return f.limits, f.err
}

func (f *operatorManagerFake) ReadObjectStoreBreaker(context.Context) (controllerwire.ObjectStoreBreaker, error) {
	f.calls = append(f.calls, "breaker")
	return f.state, f.err
}

func (f *operatorManagerFake) ResetObjectStoreBreaker(_ context.Context, key string) (controllerwire.ObjectStoreBreaker, error) {
	f.calls = append(f.calls, "reset")
	f.key = key
	return f.state, f.err
}

func operatorConnection(t *testing.T, manager *operatorManagerFake, profile string) operatorConnector {
	t.Helper()
	return func(_ context.Context, got string) (controllerwire.OperatorManager, func(), error) {
		if got != profile {
			t.Fatalf("profile = %q, want %q", got, profile)
		}
		return manager, func() { manager.calls = append(manager.calls, "close") }, nil
	}
}

func TestOperatorCommandsKeepTypedControllerProjections(t *testing.T) {
	runners, alarm := int64(0), false
	for _, format := range []string{"json", "plain", "pretty"} {
		for _, action := range []string{"show", "set"} {
			manager := &operatorManagerFake{limits: controllerwire.OperatorLimits{Limits: map[string]int64{"max_jobs_per_run": 0, "max_global_runners": 50}}}
			manager.limits.Usage.Runners, manager.limits.Usage.AlarmReached = &runners, &alarm
			args := []string{action, "--profile", "deployment", "-o", format}
			if action == "set" {
				args = append(args, "--setting", "max_jobs_per_run", "--value", "0")
			}
			var out bytes.Buffer
			err := runOperatorLimitsCommand(t.Context(), args, &out, operatorConnection(t, manager, "deployment"))
			if err != nil || len(manager.calls) != 2 || manager.calls[1] != "close" {
				t.Fatalf("limits: calls=%v error=%v", manager.calls, err)
			}
			if action == "set" && !reflect.DeepEqual(manager.write.Limits, map[string]int64{"max_jobs_per_run": 0}) {
				t.Fatalf("zero lost: %+v", manager.write)
			}
			if format == "json" {
				var got controllerwire.OperatorLimits
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, manager.limits) {
					t.Fatalf("limits changed: %s %v", &out, err)
				}
			} else if !strings.Contains(strings.ToLower(out.String()), "max_jobs_per_run") {
				t.Fatalf("setting missing: %s", &out)
			}
		}
		for _, action := range []string{"status", "reset-breaker"} {
			manager := &operatorManagerFake{state: controllerwire.ObjectStoreBreaker{Enabled: true, Classes: []controllerwire.ObjectStoreClassState{{Class: "download", PerMinute: 10, Allowed: 2}}, Ceiling: controllerwire.ObjectStoreCeiling{Incomplete: true, MeasureError: "backend unavailable"}}}
			var out bytes.Buffer
			err := runOperatorObjectStoreCommand(t.Context(), []string{action, "-o", format}, &out, operatorConnection(t, manager, ""))
			if err != nil || len(manager.calls) != 2 || manager.calls[1] != "close" {
				t.Fatalf("object store: calls=%v error=%v", manager.calls, err)
			}
			if action == "reset-breaker" && manager.key == "" {
				t.Fatal("reset has no replay identity")
			}
			if format == "json" {
				var got controllerwire.ObjectStoreBreaker
				if json.Unmarshal(out.Bytes(), &got) != nil || !reflect.DeepEqual(got, manager.state) {
					t.Fatalf("measurement changed: %s", &out)
				}
			} else if !strings.Contains(out.String(), "backend unavailable") || !strings.Contains(out.String(), "download") {
				t.Fatalf("incomplete measurement missing: %s", &out)
			}
		}
	}
}

func TestOperatorCommandPreflightHasNoSideEffects(t *testing.T) {
	for _, command := range []struct {
		run  func(context.Context, []string, io.Writer, operatorConnector) error
		read string
	}{
		{runOperatorLimitsCommand, "show"},
		{runOperatorObjectStoreCommand, "status"},
	} {
		for _, args := range [][]string{nil, {"--help"}, {command.read, "--help", "-o", "json"}} {
			var out bytes.Buffer
			if err := command.run(t.Context(), args, &out, nil); err != nil || out.Len() == 0 {
				t.Fatalf("help %v: %v %s", args, err, &out)
			}
		}
		for _, args := range [][]string{{command.read, "extra"}, {command.read, "-o", "table"}, {"unknown"}} {
			var out bytes.Buffer
			if err := command.run(t.Context(), args, &out, nil); err == nil || exitCodeFor(err) != 2 || out.Len() != 0 {
				t.Fatalf("invalid args %v: %v %s", args, err, &out)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := command.run(ctx, []string{command.read}, io.Discard, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	for _, args := range [][]string{{"set"}, {"set", "--setting", "max_jobs_per_run"}, {"set", "--setting", "max_jobs_per_run", "--value", "-1"}, {"set", "--setting", "", "--value", "1"}, {"set", "--setting", "x", "--value", "9223372036854775808"}} {
		if err := runOperatorLimitsCommand(t.Context(), args, io.Discard, nil); err == nil || exitCodeFor(err) != 2 {
			t.Fatalf("invalid setting %v: %v", args, err)
		}
	}
}

func TestOperatorCommandsReturnReadAndWriteFailures(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "deployment scope required"}
	for _, write := range []bool{false, true} {
		manager := &operatorManagerFake{err: failure}
		var out bytes.Buffer
		if err := runOperatorLimitsCommand(t.Context(), []string{"show", "-o", "json"}, &out, operatorConnection(t, manager, "")); !errors.Is(err, failure) || out.Len() != 0 {
			t.Fatalf("refusal lost: %v %s", err, &out)
		}
		manager.err = nil
		manager.limits.Limits = map[string]int64{"max_jobs_per_run": 0}
		var writer io.Writer = &out
		if write {
			writer = daemonEventsBrokenWriter{err: failure}
		}
		err := runOperatorLimitsCommand(t.Context(), []string{"show", "-o", "plain"}, writer, operatorConnection(t, manager, ""))
		if write && !errors.Is(err, failure) || !write && err != nil {
			t.Fatalf("write error lost: %v", err)
		}
	}
}

func TestOperatorCommandsCloseTransportOnCancellationAndOutputFailure(t *testing.T) {
	for _, command := range []struct {
		run  func(context.Context, []string, io.Writer, operatorConnector) error
		read string
	}{
		{runOperatorLimitsCommand, "show"},
		{runOperatorObjectStoreCommand, "status"},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		closed := false
		connect := func(context.Context, string) (controllerwire.OperatorManager, func(), error) {
			cancel()
			return &operatorManagerFake{}, func() { closed = true }, nil
		}
		if err := command.run(ctx, []string{command.read}, io.Discard, connect); !errors.Is(err, context.Canceled) || !closed {
			t.Fatalf("cancellation did not close transport: %v closed=%t", err, closed)
		}
		for _, format := range []string{"json", "pretty", "plain"} {
			failure := errors.New("output failed")
			manager := &operatorManagerFake{}
			if err := command.run(t.Context(), []string{command.read, "-o", format}, daemonEventsBrokenWriter{err: failure}, operatorConnection(t, manager, "")); !errors.Is(err, failure) || len(manager.calls) != 2 || manager.calls[1] != "close" {
				t.Fatalf("output failure lost cleanup: %v calls=%v", err, manager.calls)
			}
		}
	}
}
