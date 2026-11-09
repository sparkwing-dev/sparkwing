package jobs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArgoWaitRequiresHealthySyncedRevisionBeforeRollout(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	if err := d.assumeRole(t.Context()); err != nil {
		t.Fatal(err)
	}
	states := []string{
		`{"sync":{"revision":"old","status":"Synced"},"health":{"status":"Healthy"}}`,
		`{"sync":{"revision":"candidate","status":"OutOfSync"},"health":{"status":"Healthy"}}`,
		`{"sync":{"revision":"candidate","status":"Synced"},"health":{"status":"Progressing"}}`,
		`{"sync":{"revision":"candidate","status":"Synced"},"health":{"status":"Healthy"}}`,
	}
	reads, waits, rollouts := 0, 0, 0
	d.tools.wait = func(_ context.Context, interval time.Duration) error {
		waits++
		if interval != time.Second {
			return fmt.Errorf("Argo observation interval = %s, want 1s", interval)
		}
		return nil
	}
	run := d.tools.run
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		joined := strings.Join(c.Args, " ")
		if filepath.Base(c.Name) == "kubectl" && strings.Contains(joined, "get application ") {
			if reads >= len(states) {
				return "", errors.New("Argo did not accept the healthy candidate")
			}
			state := states[reads]
			reads++
			return `{"status":` + state + `}`, nil
		}
		if strings.Contains(joined, "rollout status") {
			if reads != len(states) {
				return "", errors.New("rollout started before Argo accepted the candidate")
			}
			rollouts++
		}
		return run(ctx, c)
	}
	err := d.waitRollout(t.Context(), d.cfg.Staging, "candidate", deployPhases[0], f.old.Controller.Digest)
	if err != nil || reads != 4 || waits != 3 || rollouts != 1 {
		t.Fatalf("Argo wait = %v, reads=%d waits=%d rollouts=%d", err, reads, waits, rollouts)
	}
}

func TestProductionSmokeInterruptsSamplingWaitAndTakesFinalSample(t *testing.T) {
	for _, failure := range []string{"", "final sample", "failed run", "warning logs", "error logs", "extra claim"} {
		t.Run(failure, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := d.assumeRole(ctx); err != nil {
				t.Fatal(err)
			}
			waiting := make(chan struct{})
			f.onSmoke = func(string) error {
				<-waiting
				if failure == "failed run" {
					return errors.New("smoke failed")
				}
				return nil
			}
			waits, samples := 0, 0
			d.tools.wait = func(ctx context.Context, _ time.Duration) error {
				waits++
				if waits > 1 {
					return errors.New("terminal smoke waited again")
				}
				close(waiting)
				<-ctx.Done()
				return ctx.Err()
			}
			run := d.tools.run
			d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
				if filepath.Base(c.Name) == "kubectl" {
					if strings.Join(c.Args, " ") == "get nodes -o json" {
						samples++
						if samples > 1 && failure == "final sample" {
							return "", errors.New("final sample failed")
						}
						if samples > 1 && failure == "extra claim" {
							f.claimsPerRun = 2
						}
					}
					if slices.Contains(c.Args, "logs") && strings.HasSuffix(failure, "logs") {
						level := "WARN"
						if failure == "error logs" {
							level = "ERROR"
						}
						return "level=" + level + " msg=failed\n", nil
					}
				}
				if slices.Contains(c.Args, "--follow") {
					if i := slices.Index(c.Args, "--poll"); i < 0 || c.Args[i+1] != "1s" {
						return "", errors.New("smoke follow is missing one-second polling")
					}
				}
				return run(ctx, c)
			}
			err := d.productionSmoke(ctx, f.candidate(), "2026-11-08T00:00:00Z")
			if (err != nil) != (failure != "") || waits != 1 || samples < 2 {
				t.Fatalf("smoke = %v, waits = %d, samples = %d", err, waits, samples)
			}
		})
	}
}
