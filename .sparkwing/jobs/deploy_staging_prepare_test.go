package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
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
