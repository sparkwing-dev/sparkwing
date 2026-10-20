package launcher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestEveryContainerRefusesExpiredLifetimeBeforeStarting(t *testing.T) {
	l, _, boundary, control := fixture()
	resources, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	pod := resources.Job.Spec.Template.Spec
	for _, container := range slices.Concat(pod.InitContainers, pod.Containers) {
		t.Run(container.Name, func(t *testing.T) {
			if !slices.ContainsFunc(container.Env, func(e corev1.EnvVar) bool {
				return e.Name == lifetimeVariable && e.ValueFrom != nil && e.ValueFrom.FieldRef.FieldPath == lifetimeField().FieldPath
			}) || !slices.Contains(container.VolumeMounts, lifetimeMount()) {
				t.Fatal("container has no read-only absolute lifetime")
			}
			args := slices.Clone(container.Command[1:])
			args[1] = strings.ReplaceAll(args[1], lifetimePath, filepath.Join(t.TempDir(), "absent"))
			// A missing executable would give 127 if preparation or execution started.
			args[len(args)-1] = "/no-such-sparkwing-entrypoint"
			command := exec.CommandContext(t.Context(), container.Command[0], append(args, container.Args...)...)
			command.Env = []string{"PATH=/bin:/usr/bin", lifetimeVariable + "=" + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)}
			assertLifetimeExit(t, command, 124)
		})
	}
}

func TestContainerLifetimeUsesOnlyProjectedAcknowledgements(t *testing.T) {
	for _, test := range []struct {
		name, projected string
		initial         time.Time
		code            int
	}{
		{"renewal", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10), time.Now().Add(-time.Hour), 0},
		{"shortened", "1", time.Now().Add(time.Hour), 124},
		{"invalid", "invalid", time.Now().Add(time.Hour), 124},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "deadline")
			if err := os.WriteFile(path, []byte(test.projected), 0o400); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "sh", "-c", strings.ReplaceAll(containerLifetime, lifetimePath, path), "lifetime", "true")
			command.Env = []string{"PATH=/bin:/usr/bin", lifetimeVariable + "=" + strconv.FormatInt(test.initial.Unix(), 10)}
			assertLifetimeExit(t, command, test.code)
		})
	}
}

func TestDockerLifetimeKillsContainersAndDaemonOnStopOrExpiry(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(strconv.FormatBool(stopped), func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range map[string]string{
				"dockerd-entrypoint.sh": "exit 0",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// Simulate an uncooperative daemon; never signal the test host's namespace.
			prefix := `kill() { if [ "$1" != -0 ]; then echo "$*" >> "$TEST_DIR/signals"; fi; }
date() { if [ -e "$TEST_DIR/ticked" ]; then echo 9999999999; else touch "$TEST_DIR/ticked"; echo 1; fi; }
sleep() { mkdir "$TEST_DIR/stop"; }
`
			if stopped {
				prefix = strings.ReplaceAll(prefix, "echo 9999999999", "echo 1")
			}
			script := prefix + strings.NewReplacer(lifetimePath, filepath.Join(dir, "absent"), DockerStopPath, filepath.Join(dir, "stop")).Replace(containerLifetime)
			command := exec.CommandContext(t.Context(), "sh", "-c", script, "lifetime", "dockerd-entrypoint.sh", "dockerd")
			command.Env = []string{"PATH=" + dir + ":/bin:/usr/bin", "TEST_DIR=" + dir, lifetimeVariable + "=99999999"}
			assertLifetimeExit(t, command, 124)
			signals, err := os.ReadFile(filepath.Join(dir, "signals"))
			if err != nil || string(signals) != "-KILL -1\n" {
				t.Fatalf("daemon namespace was not killed: %s %v", signals, err)
			}
		})
	}
}

func assertLifetimeExit(t *testing.T, command *exec.Cmd, want int) {
	t.Helper()
	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	if code != want {
		t.Fatalf("lifetime exit %d, want %d: %s", code, want, output)
	}
}

func TestDockerStopNamespaceIsPrivateToTheSidecar(t *testing.T) {
	l, _, boundary, control := fixture()
	resources, err := BuildJob(t.Context(), time.Now(), l.Config, *control.award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	pod := resources.Job.Spec.Template.Spec
	if pod.ShareProcessNamespace != nil && *pod.ShareProcessNamespace || pod.HostPID {
		t.Fatal("Docker hard stop can reach another execution namespace")
	}
	docker := pod.InitContainers[0]
	if docker.Command[0] != "sh" || docker.Command[1] != "-c" {
		t.Fatal("Docker wrapper does not own PID 1")
	}
}
