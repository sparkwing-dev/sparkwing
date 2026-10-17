package main

import (
	"strings"
	"testing"
)

func TestCompletionUsesFinalCommandAndFlagVocabulary(t *testing.T) {
	for _, test := range []struct{ kind, word, want, absent string }{
		{"verbs", "", "dashboard", "serve\t"},
		{"verbs", "", "profiles", "configure\t"},
		{"verbs", "pipeline", "trigger", "worker\t"},
		{"flags", "runs logs", "--job", "--node\t"},
		{"flags", "run", "--sw-no-memo", "--sw-no-cache"},
	} {
		out := captureStdout(t, func() {
			if err := runInternalCompleteArgs([]string{"--kind", test.kind, "--word=" + test.word, "-o", "plain"}); err != nil {
				t.Fatal(err)
			}
		})
		if !strings.Contains(out, test.want) || strings.Contains(out, test.absent) {
			t.Fatalf("completion %s %s: %s", test.kind, test.word, out)
		}
	}
}

func TestCompletionArtifactsSupportFinalShellSet(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		out := captureStdout(t, func() {
			if err := runCompletionArgs([]string{"--shell", shell, "-o", "plain"}); err != nil {
				t.Fatal(err)
			}
		})
		if out == "" || !strings.Contains(out, "--kind") {
			t.Fatalf("%s artifact is missing the internal invocation", shell)
		}
	}
}
