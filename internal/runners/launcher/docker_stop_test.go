package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerStopKillsThePrivateExecutionNamespaceAndRefusesRestart(t *testing.T) {
	for _, alreadyStopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "stopped"}[alreadyStopped], func(t *testing.T) {
			directory := t.TempDir()
			marker, signals := filepath.Join(directory, "stop"), filepath.Join(directory, "signals")
			for name, script := range map[string]string{
				"dockerd-entrypoint.sh": "#!/bin/sh\nprintf started > '" + filepath.Join(directory, "started") + "'\n",
				"sleep":                 "#!/bin/sh\nprintf '%s' \"$*\" > '" + filepath.Join(directory, "sleep") + "'\n",
			} {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if alreadyStopped {
				if err := os.Mkdir(marker, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			container := dockerContainer(CloudJobConfig{})
			script := "kill() { printf '%s\\n' \"$*\" >> '" + signals + "'; }; sleep() { mkdir '" + marker + "'; };\n" + strings.ReplaceAll(container.Command[2], DockerStopPath, marker)
			args := append([]string{container.Command[1], script, container.Command[3]}, container.Args...)
			command := exec.CommandContext(t.Context(), container.Command[0], args...)
			command.Env = []string{"PATH=" + directory + ":/bin:/usr/bin"}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("sidecar: %v: %s", err, output)
			}
			if alreadyStopped {
				if _, err := os.Stat(filepath.Join(directory, "started")); !os.IsNotExist(err) {
					t.Fatal("a stopped daemon restarted")
				}
				if got, err := os.ReadFile(filepath.Join(directory, "sleep")); err != nil || string(got) != "infinity" {
					t.Fatalf("stopped sidecar did not park: %q %v", got, err)
				}
			} else {
				got, err := os.ReadFile(signals)
				if err != nil || !strings.HasSuffix(string(got), "-KILL -1\n") {
					t.Fatalf("sidecar did not kill its execution namespace: %q %v", got, err)
				}
			}
		})
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
