//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestHostileProjectCannotAuthorizeBurst(t *testing.T) {
	for _, body := range []string{
		"route: cloud\n    profile: cloud\n    local_secrets: [TOKEN]",
		"profile: cloud",
		"prefer: [{target: cloud}]",
		"overflow_minutes: 999999",
		"local_secrets: [TOKEN]",
	} {
		t.Run(body, func(t *testing.T) { hostileProjectCannotAuthorizeBurst(t, body) })
	}
}

func FuzzHostileProjectCannotAuthorizeBurst(f *testing.F) {
	f.Add("route: cloud\n    profile: cloud\n    local_secrets: [TOKEN]")
	f.Add("prefer: [{target: cloud}]")
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1024 {
			t.Skip()
		}
		hostileProjectCannotAuthorizeBurst(t, body)
	})
}

func hostileProjectCannotAuthorizeBurst(t *testing.T, body string) {
	t.Helper()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	t.Chdir(repo)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "defaults:\n  run:\n    "+body+"\npipelines:\n  - name: burst\n    entrypoint: burst\n", 0o600)
	state, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	requests := 0
	options := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
	route, err := loadRunRouting(filepath.Join(repo, ".sparkwing"), "burst")
	local := errors.New("local execution selected")
	if err == nil {
		err = runRoutedCommand(t.Context(), "burst", runFlags{}, nil, route, io.Discard, burstRunDependencies{
			connect: func(context.Context, string) (client.EngineOptions, func(), error) { return options, func() {}, nil },
			probe:   client.CheckIdentity,
			local:   func(context.Context, string, runFlags, []string) error { return local },
			cloud: func(ctx context.Context, p string, flags runFlags, args []string, route userconfig.RunRouting, options client.EngineOptions) error {
				request, err := prepareNativeBurst(ctx, p, flags, args, route, options)
				if err != nil {
					return err
				}
				engine, err := client.NewEngineClient(options)
				if err != nil {
					return err
				}
				_, err = engine.CreateRun(ctx, request)
				return err
			},
		})
	}
	var runs, uploads int
	if e := teststore.SQL(t, state).QueryRowContext(t.Context(), "SELECT count(*) FROM runs").Scan(&runs); e != nil {
		t.Fatal(e)
	}
	if e := teststore.SQL(t, state).QueryRowContext(t.Context(), "SELECT count(*) FROM blob_records").Scan(&uploads); e != nil {
		t.Fatal(e)
	}
	if requests != 0 || runs != 0 || uploads != 0 || err == nil {
		t.Fatalf("repository authorized Cloud traffic: requests=%d runs=%d uploads=%d error=%v", requests, runs, uploads, err)
	}
}

func TestProjectUntrackedSourceRequiresMachineApproval(t *testing.T) {
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	t.Chdir(repo)
	writeSnapshotFile(t, repo, ".gitignore", "declared.txt\n", 0o600)
	state, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	options := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
	_, err := prepareNativeBurst(t.Context(), "burst", runFlags{}, nil, userconfig.RunRouting{Profile: new("cloud"), LocalSecrets: []string{"TOKEN"}}, options)
	var uploads int
	if e := teststore.SQL(t, state).QueryRowContext(t.Context(), "SELECT count(*) FROM blob_records").Scan(&uploads); e != nil {
		t.Fatal(e)
	}
	if err == nil || uploads != 0 {
		t.Fatalf("unapproved ignored source uploaded: records=%d err=%v", uploads, err)
	}
}

func TestBurstCLIRejectsNonPreferredAndTeamClaimants(t *testing.T) {
	for _, tc := range []struct{ personal, listed, allowed bool }{
		{true, false, false}, {false, false, false}, {false, true, false}, {true, true, true},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) { burstCLIClaimantBoundary(t, tc.personal, tc.listed, tc.allowed, "pool=trusted") })
	}
}

func FuzzBurstCLIClaimantsStayInsidePreferenceSet(f *testing.F) {
	f.Add(true, false, "class=cloud")
	f.Add(false, true, "class=coordinator")
	f.Fuzz(func(t *testing.T, personal, listed bool, forgedLabel string) {
		if len(forgedLabel) > 256 {
			t.Skip()
		}
		burstCLIClaimantBoundary(t, personal, listed, personal && listed, forgedLabel)
	})
}

func burstCLIClaimantBoundary(t *testing.T, personal, listed, allowed bool, forgedLabel string) {
	t.Helper()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	t.Chdir(repo)
	state, server, bearer := burstIntakeController(t, "http://controller.test")
	if _, err := teststore.SQL(t, state).ExecContext(t.Context(), "UPDATE memberships SET role='owner'"); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	options := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
	newClient := func(raw string) controllerwire.Controller {
		connection := options
		connection.Bearer = func(context.Context) (string, error) { return raw, nil }
		c, err := client.NewEngineClient(connection)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	preferences := []userconfig.RoutePreference{{Target: "here"}}
	if listed {
		preferences = append(preferences, userconfig.RoutePreference{Target: "machines"})
	}
	route := userconfig.RunRouting{Route: new("cloud"), Profile: new("cloud"), Prefer: preferences, SourceFiles: []string{"declared.txt"}, LocalSecrets: []string{"TOKEN"}}
	var created controllerwire.CreateRunResponse
	err := runRoutedCommand(t.Context(), "burst", runFlags{}, nil, route, io.Discard, burstRunDependencies{
		connect: func(context.Context, string) (client.EngineOptions, func(), error) { return options, func() {}, nil },
		probe:   client.CheckIdentity,
		local: func(context.Context, string, runFlags, []string) error {
			return errors.New("unexpected local fallback")
		},
		cloud: func(ctx context.Context, pipeline string, flags runFlags, args []string, route userconfig.RunRouting, connection client.EngineOptions) error {
			request, err := prepareNativeBurst(ctx, pipeline, flags, args, route, connection)
			if err != nil {
				return err
			}
			created, err = newClient(bearer).CreateRun(ctx, request)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerClaim, err := newClient(created.HereBearer).Claim(t.Context(), controllerwire.ClaimRequest{RunID: created.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: installedVersion()})
	if err != nil || ownerClaim.Claim == nil {
		t.Fatalf("owner: %+v %v", ownerClaim, err)
	}
	owner := newClient(ownerClaim.Claim.Capability)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: ownerClaim.Claim.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Env: ownerClaim.Claim.Request.Request.Env, Args: ownerClaim.Claim.Request.Request.RequestedArgs, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeReady), Cause: string(store.CauseRelease)}); err != nil {
		t.Fatal(err)
	}
	session, _, _, err := state.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "burst-user"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := options
	enrollment.Bearer = nil
	manager, err := client.NewSessionRunnerTokenManager(t.Context(), enrollment, session)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := manager.EnrollRunner(t.Context(), controllerwire.EnrollRunnerRequest{Name: "untrusted", Repos: []string{"example.test/acme/*"}, Personal: personal})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := newClient(machine.Capability).Claim(t.Context(), controllerwire.ClaimRequest{Kinds: []capability.UnitKind{capability.UnitNode}, RunnerVersion: installedVersion(), Labels: []string{"class=cloud", "class=coordinator", "name=here", forgedLabel}})
	if allowed {
		if err != nil || claimed.Claim == nil {
			t.Fatalf("preferred personal machine refused: %+v %v", claimed, err)
		}
		connection := options
		connection.Bearer = func(context.Context) (string, error) { return claimed.Claim.Capability, nil }
		private, err := client.NewNodeHostClient(connection, created.RunID, "job")
		if err != nil {
			t.Fatal(err)
		}
		defer private.Close()
		principal, err := private.Principal(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		process, err := private.ProcessClient(t.Context(), principal, "job")
		if err != nil {
			t.Fatal(err)
		}
		value, masked, err := process.Secret(t.Context(), "TOKEN")
		if err != nil || value != burstIntakeSecret || !masked {
			t.Fatalf("preferred secret release: masked=%v err=%v", masked, err)
		}
	} else if err != nil || claimed.Claim != nil {
		t.Fatalf("non-preferred claimant received burst unit and requester authority: personal=%v machines=%v", personal, listed)
	}
}
