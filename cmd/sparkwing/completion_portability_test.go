package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBashCompletionWithoutMapfile(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, tc := range []struct{ name, words, index, want string }{
		{"flag", "sparkwing --p", "1", "--profile"},
		{"verb", "sparkwing r", "1", "run"},
		{"leaf flags", "sparkwing leaf ''", "2", "--profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "enable -n mapfile\n" + renderBash() + `
sparkwing() {
 case "$1" in
 _complete-verbs) if [[ "$2" != leaf ]]; then printf '%s\n' run; fi ;;
 _complete-flags) printf '%s\n' --profile ;;
 esac
}
` + "COMP_WORDS=(" + tc.words + ")\nCOMP_CWORD=" + tc.index + "\n_sparkwing_complete\nprintf '%s\n' \"${COMPREPLY[@]}\"\n"
			out, err := exec.Command(bash, "--noprofile", "--norc", "-c", script).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("completion=%q err=%v want=%q", out, err, tc.want)
			}
		})
	}
}

func TestPipelineCompletionCarriesKindAndDescription(t *testing.T) {
	projectAt(t, "pipelines:\n  - name: demo\n    entrypoint: Demo\n")
	out := captureStdout(t, func() {
		if err := runInternalCompletePipelines(nil); err != nil {
			t.Fatal(err)
		}
	})
	columns := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(columns) != 4 || columns[2] != "pipeline" || columns[3] != "manual" {
		t.Fatalf("completion columns=%q", columns)
	}
}

func TestFishPipelineProjectionKeepsDescription(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	for _, line := range strings.Split(renderFish(), "\n") {
		if !strings.Contains(line, "_complete-pipelines") || !strings.Contains(line, "awk") {
			continue
		}
		_, projection, ok := strings.Cut(line, "| ")
		if !ok {
			t.Fatal("missing projection")
		}
		cmd := exec.Command(shell, "-c", projection)
		cmd.Stdin = strings.NewReader("demo\t\tpipeline\tmanual\n")
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "demo\tmanual\n" {
			t.Fatalf("projection=%q err=%v", out, err)
		}
		return
	}
	t.Fatal("missing fish pipeline projection")
}
