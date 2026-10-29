package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestEveryFlagHasAuthoredHelp(t *testing.T) {
	for _, surface := range clisurface.Registry() {
		for _, flag := range surfaceHelp(surface).Flags {
			meaning := flag.Desc
			for _, suffix := range []string{" Values: ", " Must be at least ", " Repeatable; comma-separated values are accepted."} {
				if i := strings.Index(" "+meaning, suffix); i >= 0 {
					meaning = (" " + meaning)[:i]
				}
			}
			meaning = strings.TrimSpace(meaning)
			name := strings.ReplaceAll(flag.Name, "-", " ")
			if meaning == "" || strings.EqualFold(strings.TrimSuffix(meaning, "."), name) {
				t.Errorf("%s --%s has no help text of its own (%q)", surface.Path, flag.Name, flag.Desc)
			}
		}
	}
}

func TestFlagHelpNamesOnlyFlagsItsCommandAccepts(t *testing.T) {
	mentioned := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	otherCommands := map[string]bool{
		"sparkwing pipeline new --hidden --all":        true,
		"sparkwing repos update --verify --all-files":  true,
		"sparkwing secrets set --shared --runner":      true,
		"sparkwing version hold --set --override-hold": true,
	}
	for _, surface := range clisurface.Registry() {
		accepted := map[string]bool{}
		for _, flag := range append(clisurface.GlobalFlags(), surface.Flags...) {
			accepted[flag.Name] = true
		}
		for _, flag := range surfaceHelp(surface).Flags {
			for _, m := range mentioned.FindAllStringSubmatch(flag.Desc, -1) {
				if !accepted[m[1]] && !otherCommands[surface.Path+" --"+flag.Name+" --"+m[1]] {
					t.Errorf("%s --%s help mentions --%s, which the command does not accept: %q", surface.Path, flag.Name, m[1], flag.Desc)
				}
			}
		}
	}
}
