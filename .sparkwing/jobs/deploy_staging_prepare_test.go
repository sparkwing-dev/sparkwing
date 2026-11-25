package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStagingOverlapsCLIAndRolloutAndJoinsFailures(t *testing.T) {
	for _, failure := range []string{"", "compiler", "rollout"} {
		t.Run(failure, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			compilerStarted, rolloutStarted := make(chan struct{}), make(chan struct{})
			compilerDone, rolloutDone := make(chan struct{}), make(chan struct{})
			var builds, triggers atomic.Int32
			run := d.tools.run
			d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
				switch {
				case filepath.Base(c.Name) == "go":
					builds.Add(1)
					close(compilerStarted)
					defer close(compilerDone)
					select {
					case <-rolloutStarted:
					case <-ctx.Done():
						return "", ctx.Err()
					}
					if failure == "compiler" {
						return "", errors.New("compiler failed")
					}
					if failure == "rollout" {
						<-ctx.Done()
						return "", ctx.Err()
					}
				case failure == "" && filepath.Base(c.Name) == "kubectl" && slices.Contains(c.Args, d.cfg.Staging.LauncherDeployment) && slices.Contains(c.Args, "get"):
					close(rolloutStarted)
					defer close(rolloutDone)
					select {
					case <-compilerDone:
					case <-ctx.Done():
						return "", ctx.Err()
					}
				case failure != "" && filepath.Base(c.Name) == "aws" && slices.Contains(c.Args, "assume-role-with-web-identity"):
					select {
					case <-rolloutStarted:
					default:
						close(rolloutStarted)
						defer close(rolloutDone)
						select {
						case <-compilerStarted:
						case <-ctx.Done():
							return "", ctx.Err()
						}
						if failure == "rollout" {
							return "", errors.New("rollout failed")
						}
						if failure == "compiler" {
							<-ctx.Done()
							return "", ctx.Err()
						}
					}
				case slices.Contains(c.Args, "trigger"):
					triggers.Add(1)
					for _, done := range []chan struct{}{compilerDone, rolloutDone} {
						select {
						case <-done:
						default:
							return "", errors.New("smoke submitted before preparation joined")
						}
					}
				}
				return run(ctx, c)
			}
			rec, err := d.stage(ctx, f.candidate())
			if failure != "" {
				if err == nil || !strings.Contains(err.Error(), failure+" failed") {
					t.Fatalf("stage = %v", err)
				}
				if triggers.Load() != 0 {
					t.Fatal("failed preparation submitted smoke")
				}
			} else if err != nil || rec.SmokeRun == "" || rec.ReferenceRun == "" || triggers.Load() != 2 {
				t.Fatalf("stage = %+v, %v, triggers=%d", rec, err, triggers.Load())
			}
			for _, done := range []chan struct{}{compilerDone, rolloutDone} {
				select {
				case <-done:
				default:
					t.Fatal("stage returned before preparation exited")
				}
			}
			if builds.Load() != 1 {
				t.Fatalf("CLI builds = %d, want 1", builds.Load())
			}
		})
	}
}

type observedRegistry struct {
	deployRegistry
	onTag func()
}

func (r observedRegistry) tag(ctx context.Context, password string, pin imagePin, tag string) error {
	err := r.deployRegistry.tag(ctx, password, pin, tag)
	r.onTag()
	return err
}

func TestStagingMovesPrepullTagWhenCLIBuildFailsAfterRollout(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tagged := make(chan struct{})
	d.tools.registry = observedRegistry{deployRegistry: d.tools.registry, onTag: func() { close(tagged) }}
	run := d.tools.run
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "go" {
			// The build fails only once the confirmed rollout has had its chance to move the tag.
			select {
			case <-tagged:
			case <-ctx.Done():
			}
			return "", errors.New("compiler failed")
		}
		return run(ctx, c)
	}
	images := f.candidate()
	if _, err := d.stage(ctx, images); err == nil || !strings.Contains(err.Error(), "compiler failed") {
		t.Fatalf("stage = %v, want the CLI build failure", err)
	}
	if want := []string{images.Runner.Repository + ":cloud-prepull-staging -> " + images.Runner.Digest}; !slices.Equal(f.registry.tagged, want) {
		t.Fatalf("pre-pull tag moves = %q, want %q", f.registry.tagged, want)
	}
	if runs := f.triggered("deploy-smoke"); len(runs) != 0 {
		t.Fatalf("a failed CLI build submitted smoke: %q", runs)
	}
}

func TestStagingMovesPrepullTagBetweenRolloutAndSmoke(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	d.tools.registry = observedRegistry{deployRegistry: d.tools.registry, onTag: func() { record("tag") }}
	run := d.tools.run
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if slices.Contains(c.Args, "trigger") {
			record("smoke")
		}
		out, err := run(ctx, c)
		if err == nil && filepath.Base(c.Name) == "kubectl" && slices.Contains(c.Args, d.cfg.Staging.LauncherDeployment) && slices.Contains(c.Args, "get") {
			record("rollout")
		}
		return out, err
	}
	if _, err := d.stage(t.Context(), f.candidate()); err != nil {
		t.Fatal(err)
	}
	tag := slices.Index(events, "tag")
	if tag < 0 || slices.Contains(events[tag:], "rollout") || !slices.Contains(events[:tag], "rollout") || slices.Contains(events[:tag], "smoke") || !slices.Contains(events[tag:], "smoke") {
		t.Fatalf("events = %q, want the tag move after the confirmed rollout and before the first smoke submission", events)
	}
}

func TestStagingRetryMovesPrepullTagOnlyOnceTheLiveLauncherRunsTheCandidate(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	if _, err := f.deployer(false).stage(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	moved := images.Runner.Repository + ":cloud-prepull-staging -> " + images.Runner.Digest
	for _, converged := range []bool{false, true} {
		f.registry.tagged = nil
		d := f.deployer(false)
		run := d.tools.run
		d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
			// GitOps already pins the candidate, but Argo has not rolled the launcher yet.
			if !converged && filepath.Base(c.Name) == "kubectl" && slices.Contains(c.Args, d.cfg.Staging.LauncherDeployment) && slices.Contains(c.Args, "get") {
				return testDeploymentSnapshot("launcher", f.old.Runner.ref(), true), nil
			}
			return run(ctx, c)
		}
		if _, err := d.stage(t.Context(), images); err != nil {
			t.Fatalf("retry with converged=%v: %v", converged, err)
		}
		want := []string{moved}
		if !converged {
			want = nil
		}
		if !slices.Equal(f.registry.tagged, want) {
			t.Fatalf("retry with converged=%v moved %q, want %q", converged, f.registry.tagged, want)
		}
	}
	if !slices.ContainsFunc(f.said, func(s string) bool {
		return strings.HasPrefix(s, "WARNING: staging Cloud job nodes keep pre-pulling the previous runner") && strings.Contains(s, "not "+images.Runner.Digest)
	}) {
		t.Fatalf("no warning for the unconverged launcher: %q", f.said)
	}
}

func TestStagingRetryWaitsOutALauncherRolloutArgoStartsAfterRolloutStatus(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	if _, err := f.deployer(false).stage(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	f.registry.tagged = nil
	d := f.deployer(false)
	var mu sync.Mutex
	// The live launcher still runs the previous image; Argo applies the
	// candidate template right after the first rollout status returns.
	image, complete, statuses := f.old.Runner.ref(), true, 0
	var tagWhileRolling []bool
	d.tools.registry = observedRegistry{deployRegistry: d.tools.registry, onTag: func() {
		mu.Lock()
		defer mu.Unlock()
		tagWhileRolling = append(tagWhileRolling, image != images.Runner.ref() || !complete)
	}}
	run := d.tools.run
	launcher := d.cfg.Staging.LauncherDeployment
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case filepath.Base(c.Name) == "kubectl" && slices.Contains(c.Args, "rollout") && slices.Contains(c.Args, "deployment/"+launcher):
			statuses++
			if statuses == 1 {
				image, complete = images.Runner.ref(), false
			} else {
				complete = true
			}
			return "", nil
		case filepath.Base(c.Name) == "kubectl" && slices.Contains(c.Args, "get") && slices.Contains(c.Args, launcher):
			if slices.Contains(c.Args, "json") {
				return testDeploymentSnapshot("launcher", image, complete), nil
			}
			return image, nil
		}
		mu.Unlock()
		defer mu.Lock()
		return run(ctx, c)
	}
	if _, err := d.stage(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tagWhileRolling, []bool{false}) {
		t.Fatalf("tag moves while the launcher was still rolling out = %v, want one move after the rollout completed", tagWhileRolling)
	}
}

func TestRolledOutImageRequiresTheObservedTemplateOnEveryReplica(t *testing.T) {
	const image = "registry/runner@sha256:new"
	// total is status.replicas, old and new pods together; it defaults to the spec count.
	snapshot := func(generation, observed, replicas, updated, ready, available int, total ...int) string {
		pods := replicas
		if len(total) > 0 {
			pods = total[0]
		}
		return fmt.Sprintf(`{"metadata":{"generation":%d},"spec":{"replicas":%d,"template":{"spec":{"containers":[{"name":"launcher","image":%q}]}}},`+
			`"status":{"observedGeneration":%d,"replicas":%d,"updatedReplicas":%d,"readyReplicas":%d,"availableReplicas":%d}}`,
			generation, replicas, image, observed, pods, updated, ready, available)
	}
	for name, tc := range map[string]struct {
		json     string
		complete bool
	}{
		"rolled out":            {snapshot(3, 3, 2, 2, 2, 2), true},
		"template unobserved":   {snapshot(3, 2, 2, 2, 2, 2), false},
		"replica not updated":   {snapshot(3, 3, 2, 1, 2, 2), false},
		"replica not ready":     {snapshot(3, 3, 2, 2, 1, 2), false},
		"replica unavailable":   {snapshot(3, 3, 2, 2, 2, 1), false},
		"old pod still running": {snapshot(3, 3, 1, 1, 1, 1, 2), false},
		"default one replica":   {`{"metadata":{"generation":1},"spec":{"template":{"spec":{"containers":[{"name":"launcher","image":"` + image + `"}]}}},"status":{"observedGeneration":1,"replicas":1,"updatedReplicas":1,"readyReplicas":1,"availableReplicas":1}}`, true},
	} {
		got, complete, err := rolledOutImage(tc.json, "launcher")
		if err != nil || got != image || complete != tc.complete {
			t.Errorf("%s: rolledOutImage = %q, %v, %v; want %q, %v", name, got, complete, err, image, tc.complete)
		}
	}
	if _, _, err := rolledOutImage(snapshot(1, 1, 1, 1, 1, 1), "controller"); err == nil {
		t.Error("a Deployment without the container was accepted")
	}
}

func TestConfirmRolloutKeepsOneDeadlineAcrossRolloutWatches(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	start := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := start
	d.tools.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	launcher := d.cfg.Staging.LauncherDeployment
	const rolloutTakes = 4*time.Minute + 59*time.Second
	var watches []string
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case slices.Contains(c.Args, "rollout"):
			// Each watch outlasts most of the budget; one cut short by its timeout fails as kubectl does.
			timeout := d.cfg.RolloutTimeout
			for _, arg := range c.Args {
				if v, ok := strings.CutPrefix(arg, "--timeout="); ok {
					var err error
					if timeout, err = time.ParseDuration(v); err != nil {
						t.Errorf("watch timeout %q: %v", v, err)
					}
				}
			}
			watches = append(watches, timeout.String())
			if timeout < rolloutTakes {
				clock = clock.Add(timeout)
				return "", errors.New("timed out waiting for the condition")
			}
			clock = clock.Add(rolloutTakes)
			return "", nil
		case slices.Contains(c.Args, "get") && slices.Contains(c.Args, launcher):
			// The rollout completes only after nearly two full watches.
			done := clock.Sub(start) >= 2*rolloutTakes
			return testDeploymentSnapshot("launcher", f.old.Runner.ref(), done), nil
		}
		t.Errorf("unexpected command %s %v", c.Name, c.Args)
		return "", errors.New("unexpected command")
	}
	err := d.confirmRollout(t.Context(), d.cfg.Staging, phaseNamed(deployPhaseLauncher), f.old.Runner.Digest)
	mu.Lock()
	elapsed := clock.Sub(start)
	mu.Unlock()
	if err == nil || elapsed > d.cfg.RolloutTimeout {
		t.Fatalf("confirmRollout = %v after %s with watches %q; want a failure within the %s budget", err, elapsed, watches, d.cfg.RolloutTimeout)
	}
}

func TestConfirmRolloutStopsAKubectlThatOutlivesItsTimeout(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.cfg.RolloutTimeout = time.Second
	frozen := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	d.tools.now = func() time.Time { return frozen }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		// kubectl starts --timeout only after its first lookup, so a slow lookup outlives it.
		calls.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	}
	started := time.Now()
	err := d.confirmRollout(ctx, d.cfg.Staging, phaseNamed(deployPhaseLauncher), f.old.Runner.Digest)
	elapsed := time.Since(started)
	if calls.Load() == 0 || err == nil || !strings.Contains(err.Error(), "did not finish rolling out") || elapsed > 3*time.Second {
		t.Fatalf("confirmRollout = %v after %s and %d kubectl calls; want the rollout timeout error within the %s budget", err, elapsed, calls.Load(), d.cfg.RolloutTimeout)
	}
}
