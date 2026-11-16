package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestRunRoutingPrecedence(t *testing.T) {
	cloud, local, profile := "cloud", "local", "work"
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{Route: &cloud, Profile: &profile}}}
	cfg := &projectconfig.Config{Pipelines: []projectconfig.Pipeline{{Name: "deploy", Run: &projectconfig.RunRestrictions{Route: &local}}}}
	if r := resolveRunRouting(machine, cfg, "deploy"); *r.Route != local {
		t.Fatal(r)
	}
	if r := resolveRunRouting(machine, cfg, "test"); r.Route != nil || *r.Profile != profile || r.OverflowMinutes != nil {
		t.Fatal(r)
	}
	if r := resolveRunRouting(userconfig.Config{}, nil, "test"); r.Route != nil {
		t.Fatal(r)
	}
}

func TestRepositorySecretNamesCannotChangeMachineSourcesOrClassification(t *testing.T) {
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{LocalSecrets: []string{"TOKEN", "OTHER"}}}}
	project := &projectconfig.Config{Defaults: &projectconfig.Defaults{Run: &projectconfig.RunRestrictions{LocalSecrets: []string{"TOKEN", "UNAUTHORIZED"}}}}
	route := resolveRunRouting(machine, project, "test")
	if !slices.Equal(route.LocalSecrets, []string{"TOKEN", "OTHER"}) {
		t.Fatal("repository changed machine secret sources or classification")
	}
	if !slices.Equal(machine.Run.Routing.LocalSecrets, []string{"TOKEN", "OTHER"}) {
		t.Fatal("repository modified machine configuration")
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
		{name: "unavailable during source upload", run: &burstBeforeIntakeError{&apierr.Error{Code: hosterr.Unavailable, Message: "unreachable"}}, local: true, remote: true},
		{name: "source upload forbidden", run: &burstBeforeIntakeError{&apierr.Error{Code: hosterr.Forbidden, Message: "denied"}}, remote: true},
		{name: "source upload interrupted", run: &burstBeforeIntakeError{&net.OpError{Op: "write", Net: "tcp", Err: context.Canceled}}, remote: true},
		{name: "unauthorized", probe: &apierr.Error{Code: hosterr.Forbidden, Message: "denied"}},
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
		{&apierr.Error{Code: hosterr.Unavailable, Message: "controller unavailable"}, true},
		{&apierr.Error{Code: hosterr.Forbidden, Message: "invalid profile credential"}, false},
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

func TestRoutedRunDeadTokenHintNamesTheRoutingProfile(t *testing.T) {
	cloud, profile := "cloud", "o'work"
	err := runRoutedCommand(t.Context(), "test", runFlags{}, nil, userconfig.RunRouting{Route: &cloud, Profile: &profile}, &bytes.Buffer{}, burstRunDependencies{
		connect: func(context.Context, string) (client.EngineOptions, func(), error) {
			return client.EngineOptions{}, func() {}, nil
		},
		probe: func(context.Context, client.EngineOptions) error {
			return &apierr.Error{Code: hosterr.Unauthenticated, Message: "credential is not live"}
		},
		local: func(context.Context, string, runFlags, []string) error {
			t.Fatal("dead credential fell back to a local run")
			return nil
		},
	})
	var hint string
	if writeErr := writeInvocationError(io.Discard, err, func(_ io.Writer, failure *apierr.Error) error { hint = failure.Hint; return nil }); writeErr != nil {
		t.Fatal(writeErr)
	}
	if want := "--profile 'o'\\''work' --token-stdin"; !strings.Contains(hint, want) {
		t.Fatalf("hint does not name the routing profile %q: %s", want, hint)
	}
}

func TestInvocationRoutingRequiresExplicitCloudAndPreservesMachineApprovals(t *testing.T) {
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{Route: new("local"), Profile: new("default"), LocalSecrets: []string{"TOKEN"}, SourceFiles: []string{"fixture.json"}}}}
	for _, tc := range []struct{ route, profile, wantRoute, wantProfile string }{
		{"cloud", "work", "cloud", "work"},
		{"cloud", "", "cloud", "default"},
		{"local", "", "local", "default"},
	} {
		got, err := resolveInvocationRouting(machine, nil, "gate", runFlags{route: tc.route, profile: tc.profile})
		if err != nil || *got.Route != tc.wantRoute || *got.Profile != tc.wantProfile || !slices.Equal(got.LocalSecrets, []string{"TOKEN"}) || !slices.Equal(got.SourceFiles, []string{"fixture.json"}) {
			t.Fatalf("route=%+v err=%v", got, err)
		}
	}
	if *machine.Run.Routing.Route != "local" || *machine.Run.Routing.Profile != "default" {
		t.Fatal("invocation changed machine defaults")
	}
	for _, project := range []*projectconfig.Config{
		{Defaults: &projectconfig.Defaults{Run: &projectconfig.RunRestrictions{Route: new("local")}}},
		{Pipelines: []projectconfig.Pipeline{{Name: "gate", Run: &projectconfig.RunRestrictions{Route: new("local")}}}},
	} {
		if _, err := resolveInvocationRouting(machine, project, "gate", runFlags{route: "cloud", profile: "work"}); err == nil {
			t.Fatal("invocation bypassed repository local restriction")
		}
	}
}

func TestExplicitCloudRouteDetachesAndNeverFallsBack(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, failure := range []error{nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}, &burstBeforeIntakeError{&apierr.Error{Code: hosterr.Unavailable, Message: "offline"}}} {
			flags, _ := parseRunFlagsArgs([]string{"--sw-route=cloud", "--profile=work", fmt.Sprintf("--sw-detached=%t", detached)})
			called := false
			err := runRoutedCommand(t.Context(), "gate", flags, nil, userconfig.RunRouting{Route: new("cloud"), Profile: new("work")}, io.Discard, burstRunDependencies{
				connect: func(context.Context, string) (client.EngineOptions, func(), error) {
					return client.EngineOptions{}, func() {}, nil
				},
				probe: func(context.Context, client.EngineOptions) error { return nil },
				cloud: func(_ context.Context, _ string, got runFlags, _ []string, _ userconfig.RunRouting, _ client.EngineOptions) error {
					called = true
					if got.detached != detached {
						t.Fatal("detachment lost")
					}
					return failure
				},
				local: func(context.Context, string, runFlags, []string) error {
					t.Fatal("explicit Cloud fell back locally")
					return nil
				},
			})
			if !called || (err == nil) != (failure == nil) {
				t.Fatalf("called=%v err=%v", called, err)
			}
		}
	}
}

func TestBareRunStaysLocalAndBurstNeedsAnExplicitRoute(t *testing.T) {
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{Route: new("cloud"), Profile: new("far"), Prefer: []userconfig.RoutePreference{{Target: "here", Hold: "5s"}, {Target: "cloud"}}}}}
	bare, err := resolveInvocationRouting(machine, nil, "gate", runFlags{})
	if err != nil || bare.Route != nil {
		t.Fatalf("a bare run routed to %v: %v", bare.Route, err)
	}
	calls := map[string]int{}
	deps := burstRunDependencies{
		connect: func(context.Context, string) (client.EngineOptions, func(), error) {
			calls["connect"]++
			return client.EngineOptions{}, func() {}, nil
		},
		probe: func(context.Context, client.EngineOptions) error { return nil },
		local: func(context.Context, string, runFlags, []string) error { calls["local"]++; return nil },
		cloud: func(context.Context, string, runFlags, []string, userconfig.RunRouting, client.EngineOptions) error {
			calls["cloud"]++
			return nil
		},
	}
	if err := runRoutedCommand(t.Context(), "gate", runFlags{}, nil, bare, io.Discard, deps); err != nil || calls["local"] != 1 || calls["connect"] != 0 {
		t.Fatalf("bare run: %v %v", calls, err)
	}
	burst, err := resolveInvocationRouting(machine, nil, "gate", runFlags{route: "burst"})
	if err != nil || burst.Route == nil || *burst.Route != "cloud" || *burst.Profile != "far" || len(burst.Prefer) != 2 {
		t.Fatalf("--sw-route burst did not apply machine routing: %+v %v", burst, err)
	}
	if err := runRoutedCommand(t.Context(), "gate", runFlags{route: "burst"}, nil, burst, io.Discard, deps); err != nil || calls["cloud"] != 1 {
		t.Fatalf("burst run: %v %v", calls, err)
	}
	if err := validateLocalRunFlags(runFlags{route: "burst"}); err != nil {
		t.Fatalf("--sw-route burst refused: %v", err)
	}
	restricted := &projectconfig.Config{Defaults: &projectconfig.Defaults{Run: &projectconfig.RunRestrictions{Route: new("local")}}}
	if _, err := resolveInvocationRouting(machine, restricted, "gate", runFlags{route: "burst"}); err == nil {
		t.Fatal("--sw-route burst bypassed repository local restriction")
	}
}

func TestManagedHookRunsStayLocalUnderMachineRouting(t *testing.T) {
	t.Setenv("SPARKWING_PROFILE", "far")
	machine := userconfig.Config{Run: &userconfig.RunConfig{Routing: &userconfig.RunRouting{Route: new("cloud"), Profile: new("far"), Prefer: []userconfig.RoutePreference{{Target: "cloud"}}}}}
	script := renderHookScript("pre-push", []string{"gate"})
	runs := 0
	for _, line := range strings.Split(script, "\n") {
		words, ok := strings.CutPrefix(line, "sparkwing run ")
		if !ok {
			continue
		}
		runs++
		inv, err := clisurface.Parse("sparkwing", append([]string{"run"}, strings.Fields(strings.ReplaceAll(words, "'", ""))...))
		if err != nil {
			t.Fatalf("hook line %q: %v", line, err)
		}
		flags, _ := runFlagsFromInvocation(inv)
		route, err := resolveInvocationRouting(machine, nil, inv.Pipeline, flags)
		if err != nil || route.Route != nil {
			t.Fatalf("hook line %q routed to %v: %v", line, route.Route, err)
		}
	}
	if runs != 1 {
		t.Fatalf("hook script has %d run lines:\n%s", runs, script)
	}
}
