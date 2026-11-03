package main

import (
	"strings"
	"testing"
)

func TestResolveRunAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flag    string
		flagSet bool
		env     map[string]string
		want    string
		err     string
	}{
		{name: "no agent environment", want: ""},
		{name: "claude code", env: map[string]string{"CLAUDECODE": "1"}, want: "claude-code"},
		{name: "claude code needs its documented value", env: map[string]string{"CLAUDECODE": "0"}, want: ""},
		{name: "codex", env: map[string]string{"CODEX_THREAD_ID": "019a"}, want: "codex"},
		{name: "gemini cli", env: map[string]string{"GEMINI_CLI": "1"}, want: "gemini-cli"},
		{name: "codex launched from claude code", env: map[string]string{"CLAUDECODE": "1", "CODEX_THREAD_ID": "019a"}, want: "codex"},
		{name: "environment names the agent", env: map[string]string{"CLAUDECODE": "1", "SPARKWING_AGENT": "claude-code:land-agent"}, want: "claude-code:land-agent"},
		{name: "environment opts out", env: map[string]string{"CLAUDECODE": "1", "SPARKWING_AGENT": "none"}, want: ""},
		{name: "flag wins", flag: "claude-code:review", flagSet: true, env: map[string]string{"CLAUDECODE": "1", "SPARKWING_AGENT": "codex"}, want: "claude-code:review"},
		{name: "flag opts out", flag: "none", flagSet: true, env: map[string]string{"CLAUDECODE": "1"}, want: ""},
		{name: "unsafe flag", flag: "bad label", flagSet: true, err: "--agent"},
		{name: "unsafe environment", env: map[string]string{"SPARKWING_AGENT": "bad\x1b[0m"}, err: "SPARKWING_AGENT"},
		{name: "empty flag", flag: "", flagSet: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRunAgent(tc.flag, tc.flagSet, func(name string) string { return tc.env[name] })
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one naming %s", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveRunAgent = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
