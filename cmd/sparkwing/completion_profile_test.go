package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestProfileCompletionUsesFinalMachineConnections(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  zebra: {controller: {url: https://z.example, token: private-credential}}\n  alpha: {controller: {url: https://a.example}}\n")
	var out strings.Builder
	if err := runInternalCompleteProfiles(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "alpha\nzebra\n" {
		t.Fatalf("profile candidates: %q", out.String())
	}
	if err := runInternalCompleteProfiles(doctorFailedWriter{}, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("completion write error: %v", err)
	}
}

func TestProfileCompletionRefusesInvalidMachineSettings(t *testing.T) {
	writeProfilesFixture(t, "profiles:\n  prod: {controller: {url: https://api.example}}\nrunner: {allow_repos: []}\n")
	var out strings.Builder
	if err := runInternalCompleteProfiles(&out, nil); err != nil || out.Len() != 0 {
		t.Fatalf("invalid machine settings produced candidates: %q, %v", out.String(), err)
	}
}

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
			for _, stale := range []string{"--sw-profile"} {
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
