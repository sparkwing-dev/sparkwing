package jobs

import (
	"context"
	"errors"
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
				return f.old.Runner.ref(), nil
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
