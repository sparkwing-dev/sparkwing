package cluster

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
)

func TestLauncherCommandPassesOnlyOperatorConfiguration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, agentTokenCredential), []byte("private-launcher-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundary := errors.New("fixture boundary")
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	args := []string{"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=500m", "--memory-ceiling=1Gi", "--credentials-dir=" + dir, "--label=cluster", "--stop-team=team,another"}
	if _, err := runnerflags.Parse("launch", args, io.Discard); err != nil {
		t.Fatal(err)
	}
	err := RunLaunchCLI(t.Context(), args,
		func(_ context.Context, cfg launcher.CloudJobConfig, bearer func(context.Context) (string, error)) (*launcher.Launcher, error) {
			token, err := bearer(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, agentTokenCredential), []byte("rotated-root"), 0o600); err != nil {
				t.Fatal(err)
			}
			rotated, err := bearer(t.Context())
			if err != nil || rotated != "rotated-root" {
				t.Fatalf("credential rotation requires restart: %q %v", rotated, err)
			}
			if len(cfg.StoppedTeams) != 2 || cfg.StoppedTeams[0] != "team" || cfg.StoppedTeams[1] != "another" || len(cfg.Labels) != 1 || cfg.Labels[0] != "cluster" || cfg.CPUCeiling != .5 || cfg.MemoryCeiling != 1<<30 || cfg.Namespace != "sparkwing-jobs" || cfg.DockerImage != image || token != "private-launcher-root" {
				t.Fatalf("configuration lost at runtime boundary")
			}
			return nil, boundary
		})
	if !errors.Is(err, boundary) {
		t.Fatalf("command error: %v", err)
	}
}

func TestLauncherCommandRejectsRemovedCacheAndScratchOptions(t *testing.T) {
	for _, option := range []string{"--cache=http://cache", "--gitcache=http://cache", "--dependency-proxy=http://proxy", "--scratch=40Gi", "--deadline=1h"} {
		if err := RunLaunchCLI(t.Context(), []string{option}, nil); err == nil {
			t.Fatalf("accepted %s", option)
		}
	}
}

func TestLauncherCommandRejectsMemoryOverflowBeforeRuntime(t *testing.T) {
	image := "example.com/image@sha256:" + strings.Repeat("a", 64)
	for _, memory := range []string{"18446744073709551617", "-18446744073709551615", "9223372036854775808"} {
		t.Run(memory, func(t *testing.T) {
			err := RunLaunchCLI(t.Context(), []string{"--controller=https://controller.example", "--image=" + image, "--docker-image=" + image, "--cpu-ceiling=1", "--memory-ceiling=" + memory, "--credentials-dir=" + t.TempDir()}, func(context.Context, launcher.CloudJobConfig, func(context.Context) (string, error)) (*launcher.Launcher, error) {
				t.Error("overflow reached runtime factory")
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), "--memory-ceiling") {
				t.Fatalf("quantity error: %v", err)
			}
		})
	}
}
