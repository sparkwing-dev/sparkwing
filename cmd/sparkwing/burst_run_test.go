package main

import (
	"bytes"
	"context"
	"errors"
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

func TestBurstRouteFallsBackOnlyBeforeIntake(t *testing.T) {
	cloud, profile := "cloud", "work"
	for _, tc := range []struct {
		name          string
		probe, run    error
		local, remote bool
	}{
		{name: "online", remote: true},
		{name: "offline", probe: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}, local: true},
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
			if (tc.probe != nil && !tc.local || tc.run != nil) && err == nil {
				t.Fatal("failure lost")
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
