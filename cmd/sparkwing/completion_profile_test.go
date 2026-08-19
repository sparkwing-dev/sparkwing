package main

import (
	"strings"
	"testing"
)

func TestShellCompletionUsesLiveProfileFlag(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{name: "bash", script: renderBash(), want: `if [[ "$prev" == "--profile" ]]`},
		{name: "zsh", script: renderZsh(), want: `"${words[CURRENT-1]}" == "--profile"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.script, tt.want) {
				t.Fatalf("completion does not handle live profile flag %q", tt.want)
			}
			for _, stale := range []string{"--sw-profile", "_complete-profiles-for-pipeline"} {
				if strings.Contains(tt.script, stale) {
					t.Errorf("completion contains retired profile selection %q", stale)
				}
			}
		})
	}
}

func TestFishProfileFlagsCompleteProfileNames(t *testing.T) {
	const candidates = `-a '(__sparkwing_profiles)'`
	found := 0
	for _, line := range strings.Split(renderFish(), "\n") {
		if !strings.HasPrefix(line, "complete -c sparkwing ") || !strings.Contains(line, " -l profile") {
			continue
		}
		found++
		if !strings.Contains(line, candidates) {
			t.Errorf("profile completion has no candidates: %s", line)
		}
	}
	if found == 0 {
		t.Fatal("fish completion contains no profile flags")
	}
}
