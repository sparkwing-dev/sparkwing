package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestRunRoutingPrecedence(t *testing.T) {
	cloud, local, profile := "cloud", "local", "work"
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{Route: &cloud, Profile: &profile}}}
	cfg := &projectconfig.Config{Defaults: &projectconfig.Defaults{Run: &userconfig.RunRouting{OverflowMinutes: new(int64)}}, Pipelines: []projectconfig.Pipeline{{Name: "deploy", Run: &userconfig.RunRouting{Route: &local}}}}
	if r := resolveRunRouting(machine, cfg, "deploy"); *r.Route != local {
		t.Fatal(r)
	}
	if r := resolveRunRouting(machine, cfg, "test"); *r.Route != cloud || r.OverflowMinutes == nil {
		t.Fatal(r)
	}
	if r := resolveRunRouting(userconfig.Config{}, nil, "test"); r.Route != nil {
		t.Fatal(r)
	}
}

func FuzzBurstRouteNeverDuplicatesPossibleIntake(f *testing.F) {
	f.Add("connection lost", false, false)
	f.Add("connection lost", true, false)
	f.Add("interrupted", true, true)
	f.Fuzz(func(t *testing.T, message string, beforeIntake, cancelled bool) {
		if len(message) > 1024 {
			t.Skip()
		}
		cloud, profile := "cloud", "work"
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		localCalls, cloudCalls := 0, 0
		var out bytes.Buffer
		err := runRoutedCommand(ctx, "test", runFlags{}, nil, userconfig.RunRouting{Route: &cloud, Profile: &profile}, &out, burstRunDependencies{
			connect: func(context.Context, string) (client.EngineOptions, func(), error) {
				return client.EngineOptions{}, func() {}, nil
			},
			probe: func(context.Context, client.EngineOptions) error { return nil },
			local: func(context.Context, string, runFlags, []string) error { localCalls++; return nil },
			cloud: func(context.Context, string, runFlags, []string, userconfig.RunRouting, client.EngineOptions) error {
				cloudCalls++
				var cause error = errors.New(message)
				if cancelled && len(message)%2 == 0 {
					cancel()
				} else if cancelled {
					cause = context.Canceled
				}
				var failure error = &net.OpError{Op: "write", Net: "tcp", Err: cause}
				if beforeIntake {
					failure = &burstBeforeIntakeError{failure}
				}
				return fmt.Errorf("prepare or submit: %w", failure)
			},
		})
		if cloudCalls != 1 || localCalls > 1 || localCalls == 1 != (beforeIntake && !cancelled) {
			t.Fatalf("possible duplicate owner: before=%v cancelled=%v local=%d cloud=%d", beforeIntake, cancelled, localCalls, cloudCalls)
		}
		if (err == nil) != (localCalls == 1) || localCalls == 1 && out.Len() == 0 {
			t.Fatalf("missing failure or fallback notice: local=%d err=%v", localCalls, err)
		}
	})
}

func TestBurstRouteFallsBackOnlyBeforeIntake(t *testing.T) {
	cloud, profile := "cloud", "work"
	for _, tc := range []struct {
		name          string
		probe, run    error
		local, remote bool
	}{
		{name: "online", remote: true},
		{name: "offline", probe: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}, local: true},
		{name: "offline during source upload", run: &burstBeforeIntakeError{&net.OpError{Op: "write", Net: "tcp", Err: errors.New("unreachable")}}, local: true, remote: true},
		{name: "unavailable during source upload", run: &burstBeforeIntakeError{&apierr.Error{Code: apierr.Unavailable, Message: "unreachable"}}, local: true, remote: true},
		{name: "source upload forbidden", run: &burstBeforeIntakeError{&apierr.Error{Code: apierr.Forbidden, Message: "denied"}}, remote: true},
		{name: "source upload interrupted", run: &burstBeforeIntakeError{&net.OpError{Op: "write", Net: "tcp", Err: context.Canceled}}, remote: true},
		{name: "unauthorized", probe: &apierr.Error{Code: apierr.Forbidden, Message: "denied"}},
		{name: "lost intake response", run: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("lost response")}, remote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			localCalled, remoteCalled := false, false
			deps := burstRunDependencies{
				connect: func(context.Context, string) (client.EngineOptions, func(), error) {
					return client.EngineOptions{}, func() {}, nil
				},
				probe: func(context.Context, client.EngineOptions) error { return tc.probe },
				local: func(context.Context, string, runFlags, []string) error { localCalled = true; return nil },
				cloud: func(context.Context, string, runFlags, []string, userconfig.RunRouting, client.EngineOptions) error {
					remoteCalled = true
					return tc.run
				},
			}
			err := runRoutedCommand(t.Context(), "test", runFlags{}, nil, userconfig.RunRouting{Route: &cloud, Profile: &profile}, &out, deps)
			if localCalled != tc.local || remoteCalled != tc.remote {
				t.Fatalf("local=%v cloud=%v err=%v", localCalled, remoteCalled, err)
			}
			if tc.local && out.Len() == 0 {
				t.Fatal("fallback was silent")
			}
			if (tc.probe != nil || tc.run != nil) && !tc.local && err == nil {
				t.Fatal("failure lost")
			}
			if tc.local && err != nil {
				t.Fatalf("local fallback failed: %v", err)
			}
		})
	}
}

func TestHereRuntimeHasProductionControllerAndHostBinding(t *testing.T) {
	runtime, err := resolveHereRuntime(t.Context(), client.EngineOptions{URL: "https://cloud.example"})
	if err != nil || runtime.controller == nil || runtime.bind == nil {
		t.Fatalf("here runtime: %+v %v", runtime, err)
	}
}

func TestBurstRejectsCommitSelectionBeforeConnecting(t *testing.T) {
	cloud, profile := "cloud", "work"
	for _, flags := range []runFlags{{ref: "other"}, {pipelineRef: "other"}} {
		called := false
		err := runRoutedCommand(t.Context(), "test", flags, nil, userconfig.RunRouting{Route: &cloud, Profile: &profile}, &bytes.Buffer{}, burstRunDependencies{connect: func(context.Context, string) (client.EngineOptions, func(), error) {
			called = true
			return client.EngineOptions{}, func() {}, nil
		}})
		if err == nil || called {
			t.Fatalf("source selection silently ignored: connected=%v err=%v", called, err)
		}
	}
}

func TestBurstConnectionFailureFallsBackBeforeIntake(t *testing.T) {
	cloud, profile := "cloud", "work"
	for _, tc := range []struct {
		failure error
		local   bool
	}{
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("controller unreachable")}, true},
		{&apierr.Error{Code: apierr.Unavailable, Message: "controller unavailable"}, true},
		{&apierr.Error{Code: apierr.Forbidden, Message: "invalid profile credential"}, false},
		{context.Canceled, false},
	} {
		t.Run(tc.failure.Error(), func(t *testing.T) {
			local := false
			var out bytes.Buffer
			err := runRoutedCommand(t.Context(), "test", runFlags{}, nil, userconfig.RunRouting{Route: &cloud, Profile: &profile}, &out, burstRunDependencies{
				connect: func(context.Context, string) (client.EngineOptions, func(), error) {
					return client.EngineOptions{}, nil, tc.failure
				},
				probe: func(context.Context, client.EngineOptions) error {
					t.Fatal("failed connection reached probe")
					return nil
				},
				local: func(context.Context, string, runFlags, []string) error { local = true; return nil },
			})
			if local != tc.local || (err == nil) != tc.local || (out.Len() > 0) != tc.local {
				t.Fatalf("local=%v notice=%q error=%v", local, &out, err)
			}
		})
	}
}
