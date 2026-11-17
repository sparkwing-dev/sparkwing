package clisurface

import (
	"errors"
	"strings"
	"testing"
)

func TestReadVerbsHardCut(t *testing.T) {
	for _, tc := range []struct {
		group string
		old   string
		verb  string
		flags []string
	}{
		{"pipeline", "describe", "show", []string{"--pipeline", "build"}},
		{"repos", "info", "show", nil},
		{"cache", "info", "status", nil},
		{"secrets", "get", "show", []string{"--secret", "REGION"}},
		{"debug", "env", "show", []string{"--run", "r1", "--job", "build"}},
	} {
		t.Run(tc.group, func(t *testing.T) {
			path := "sparkwing " + tc.group + " " + tc.verb
			for _, format := range []string{"pretty", "json", "plain"} {
				args := append([]string{tc.group, tc.verb}, tc.flags...)
				args = append(args, "-o", format)
				inv, err := Parse("sparkwing", args)
				if err != nil || inv.Command.Path != path || inv.Output(false) != format {
					t.Errorf("%v: got %q, %v", args, inv.Command.Path, err)
				}
			}
			for _, flags := range [][]string{tc.flags, {"--help"}, {"--output", "invalid"}} {
				args := append([]string{tc.group, tc.old}, flags...)
				_, err := Parse("sparkwing", args)
				var usage *UsageError
				if !errors.As(err, &usage) || usage.exitCode() != ExitUsage || !strings.Contains(usage.Message, "did you mean "+path+"?") {
					t.Errorf("%v: got %v, want usage refusal naming %s", args, err, path)
				}
			}
		})
	}
}
