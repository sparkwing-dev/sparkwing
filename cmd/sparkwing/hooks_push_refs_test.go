package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

func TestRenderedPrePushHookCapturesRefsForEveryPipeline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook")
	}
	const object = "1111111111111111111111111111111111111111"
	const old = "2222222222222222222222222222222222222222"
	for _, tc := range []struct{ name, refs string }{
		{"branch", "refs/heads/topic " + object + " refs/heads/topic " + old + "\n"},
		{"main", "refs/heads/main " + object + " refs/heads/main " + old + "\n"},
		{"refspec", "refs/heads/topic " + object + " refs/heads/main " + old + "\n"},
		{"multiple", "refs/heads/topic " + object + " refs/heads/topic " + old + "\nrefs/heads/release " + object + " refs/heads/main " + old + "\n"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			writeExec(t, filepath.Join(bin, "sparkwing"), "#!/bin/sh\nprintf '<%s>\\n' \"${"+gitenv.PushRefsVar+"-unset}\"\ncat\n")
			command := exec.CommandContext(t.Context(), "sh", "-c", renderHookScript("pre-push", []string{"first", "second"}))
			command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
			command.Stdin = strings.NewReader(tc.refs)
			raw, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("hook: %v\n%s", err, raw)
			}
			want := "<" + strings.TrimRight(tc.refs, "\n") + ">\n"
			if string(raw) != want+want {
				t.Fatalf("pipeline ref captures = %q, want %q twice", raw, want)
			}
		})
	}
}

func TestRenderedOtherHooksLeaveStdinForTheirPipelines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook")
	}
	for _, hook := range []string{"pre-commit", "post-commit", "commit-msg"} {
		t.Run(hook, func(t *testing.T) {
			bin := t.TempDir()
			writeExec(t, filepath.Join(bin, "sparkwing"), "#!/bin/sh\nprintf '<%s>\\n' \"${"+gitenv.PushRefsVar+"-unset}\"\ncat\n")
			command := exec.CommandContext(t.Context(), "sh", "-c", renderHookScript(hook, []string{"gate"}))
			command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
			command.Stdin = strings.NewReader("hook input\n")
			raw, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("hook: %v\n%s", err, raw)
			}
			if string(raw) != "<unset>\nhook input\n" {
				t.Fatalf("pipeline input = %q", raw)
			}
		})
	}
}

func TestRenderedPrePushHookRejectsFailedRefCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook")
	}
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "cat"), "#!/bin/sh\nexit 37\n")
	writeExec(t, filepath.Join(bin, "sparkwing"), "#!/bin/sh\nprintf 'pipeline ran\\n'\n")
	command := exec.CommandContext(t.Context(), "sh", "-c", renderHookScript("pre-push", []string{"gate"}))
	command.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	raw, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("hook accepted failed ref capture: %s", raw)
	}
	if len(raw) != 0 {
		t.Fatalf("pipeline ran after failed ref capture: %s", raw)
	}
}
