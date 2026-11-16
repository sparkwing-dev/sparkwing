package wingd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestLocalRunnerSystemdAppliesEnforcedBudget(t *testing.T) {
	limitArgs := []string{"--property=CPUQuota=200%", "--property=MemoryMax=1073741824", "--nice=10"}
	for _, tc := range []struct {
		name   string
		limits RunnerLimits
		want   []string
	}{
		{"enforced", RunnerLimits{CPUCores: 2, MemoryBytes: 1 << 30, Nice: 10}, limitArgs},
		{"cpu only", RunnerLimits{CPUCores: 2.5, Nice: 10}, []string{"--property=CPUQuota=250%", "--nice=10"}},
		{"unenforced", RunnerLimits{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := runnerSupervisorHost(t)
			var launch []string
			host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
				if name == "systemd-run" {
					launch = slices.Clone(args)
				}
				return "", nil
			}
			if err := startLocalRunnerLinux(t.Context(), host, "instance", t.TempDir(), tc.limits); err != nil {
				t.Fatal(err)
			}
			separator := slices.Index(launch, "--")
			if separator < 0 {
				t.Fatalf("no argv separator in %q", launch)
			}
			var got []string
			for _, arg := range launch[:separator] {
				if strings.HasPrefix(arg, "--property=CPUQuota=") || strings.HasPrefix(arg, "--property=MemoryMax=") || strings.HasPrefix(arg, "--nice=") {
					got = append(got, arg)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("limit arguments = %q; want %q in %q", got, tc.want, launch)
			}
		})
	}
}

func TestLocalRunnerStartsWithoutLimitsTheManagerRefuses(t *testing.T) {
	host := runnerSupervisorHost(t)
	var launches [][]string
	host.Exec = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "systemd-run" {
			return "", nil
		}
		launches = append(launches, slices.Clone(args))
		if slices.Contains(args, "--property=CPUQuota=200%") {
			return "Unknown assignment", errors.New("exit status 1")
		}
		return "", nil
	}
	if err := startLocalRunnerLinux(t.Context(), host, "instance", t.TempDir(), RunnerLimits{CPUCores: 2, Nice: 10}); err != nil {
		t.Fatalf("a refused limit stopped the runner: %v", err)
	}
	if len(launches) != 2 || slices.Contains(launches[1], "--nice=10") || slices.Contains(launches[1], "--property=CPUQuota=200%") {
		t.Fatalf("launches = %q; want one limited attempt, then one without limits", launches)
	}
}

func TestLocalRunnerLimitsFollowOnlyAnEnforcingBudget(t *testing.T) {
	for raw, want := range map[string]func(RunnerLimits) bool{
		"2":             func(l RunnerLimits) bool { return l == RunnerLimits{} },
		"0.5,enforce":   func(l RunnerLimits) bool { return l == RunnerLimits{CPUCores: 0.5, Nice: backgroundNice} },
		"64MiB,enforce": func(l RunnerLimits) bool { return l == RunnerLimits{MemoryBytes: 64 << 20, Nice: backgroundNice} },
		"0.5,64MiB,enforce": func(l RunnerLimits) bool {
			return l == RunnerLimits{CPUCores: 0.5, MemoryBytes: 64 << 20, Nice: backgroundNice}
		},
		"ignore-external": func(l RunnerLimits) bool { return l == RunnerLimits{} },
	} {
		budget, err := userconfig.ParseBudget(raw)
		if err != nil {
			t.Fatal(err)
		}
		limits, err := LocalRunnerLimits(budget)
		if err != nil {
			t.Fatal(err)
		}
		if !want(limits) {
			t.Errorf("budget %q gave runner limits %+v", raw, limits)
		}
	}
}

func TestRunnerEnforcementReadsLimitsBackFromTheRunnerCgroup(t *testing.T) {
	root := t.TempDir()
	unit := filepath.Join(root, "sys", "fs", "cgroup", "user.slice", "app.slice", "sparkwing-runner-x.service")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "proc", "42", "cgroup"), "0::/user.slice/app.slice/sparkwing-runner-x.service\n")
	write(filepath.Join(root, "proc", "42", "stat"), "42 (sparkwing (runner) x) S 1 42 42 0 -1 4194560 1 0 0 0 0 0 0 0 30 10 8 0 1 0\n")
	write(filepath.Join(unit, "cpu.max"), "200000 100000\n")
	write(filepath.Join(unit, "memory.max"), "max\n")
	write(filepath.Join(root, "sys", "fs", "cgroup", "user.slice", "memory.max"), "1073741824\n")
	sensor := newContainerSensor(root)
	budget := func(raw string) userconfig.Budget {
		b, err := userconfig.ParseBudget(raw)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cores, memory := sensor.processLimits(42)
	nice, ok := processNice(root, 42)
	if cores != 2 || memory != 1<<30 || !ok || nice != 10 {
		t.Fatalf("read back cores=%v memory=%d nice=%d ok=%v", cores, memory, nice, ok)
	}
	if got := runnerEnforcement(budget("2,1GiB,enforce"), 16, 32<<30, cores, memory, nice); !got.Applied || got.Reason != "" || got.CPUCores != 2 || got.Nice != 10 {
		t.Fatalf("applied limits judged %+v", got)
	}

	write(filepath.Join(unit, "cpu.max"), "max 100000\n")
	cores, memory = sensor.processLimits(42)
	got := runnerEnforcement(budget("2,enforce"), 16, 32<<30, cores, memory, nice)
	if got.Applied || got.Reason != "the local runner's cgroup has no CPU limit" {
		t.Fatalf("an absent CPU limit judged %+v", got)
	}
	if got := runnerEnforcement(budget("2,enforce"), 16, 32<<30, 8, 0, 10); got.Applied || !strings.Contains(got.Reason, "8.0 cores exceeds the 2.0-core budget") {
		t.Fatalf("a looser CPU limit judged %+v", got)
	}
	if got := runnerEnforcement(budget("2,2GiB,enforce"), 16, 32<<30, 2, 0, 10); got.Applied || got.Reason != "the local runner's cgroup has no memory limit" {
		t.Fatalf("an absent memory limit judged %+v", got)
	}
}
