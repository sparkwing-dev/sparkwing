package main

import (
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
