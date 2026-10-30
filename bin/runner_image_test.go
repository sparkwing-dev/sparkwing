package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunnerImageRequiresDockerAndItsPlugins(t *testing.T) {
	for _, missing := range []string{"", "--version", "buildx version", "compose version"} {
		t.Run("missing="+missing, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\ncase ${0##*/} in\ngo) echo " + runtime.GOARCH + ";;\ngit) echo \"$RUNNER_IMAGE_FIXTURE\";;\ndocker) [ \"$*\" != \"$MISSING_DOCKER_COMMAND\" ];;\nesac\n"
			for _, tool := range []string{"bash", "git", "git-daemon", "ssh", "curl", "tar", "gzip", "xz", "make", "jq", "unzip", "go", "nc", "nsenter", "date", "sort", "timeout", "docker"} {
				if tool == "docker" && missing == "--version" {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"grep", "uname"} {
				path, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
					t.Fatal(err)
				}
			}
			tools := filepath.Join(dir, "tools")
			if err := os.WriteFile(tools, []byte("go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "check-runner-image.sh")
			cmd.Env = append(os.Environ(), "PATH="+dir, "RUNNER_TOOLS="+tools, "RUNNER_IMAGE_FIXTURE="+dir, "MISSING_DOCKER_COMMAND="+missing)
			out, err := cmd.CombinedOutput()
			if (err != nil) != (missing != "") {
				t.Fatalf("missing=%q err=%v output=%s", missing, err, out)
			}
		})
	}
}
