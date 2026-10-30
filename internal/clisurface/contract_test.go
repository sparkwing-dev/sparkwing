package clisurface

import (
	"slices"
	"strings"
	"testing"
)

func TestRegistryIntegrity(t *testing.T) {
	if err := validate(Registry()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRejectsGrammarDrift(t *testing.T) {
	tests := []struct {
		name   string
		change func([]Command) []Command
	}{
		{"duplicate path", func(c []Command) []Command { return append(c, c[0]) }},
		{"missing parent", func(c []Command) []Command {
			return slices.DeleteFunc(c, func(v Command) bool { return v.Path == "sparkwing pipeline" })
		}},
		{"extra positional", func(c []Command) []Command { c[0].PipelineArgument = true; return c }},
		{"q as query", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "search", Short: "q", Argument: "TEXT"})
			return c
		}},
		{"duplicate global", func(c []Command) []Command { c[0].Flags = append(c[0].Flags, Flag{Name: "output"}); return c }},
		{"zero page", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "limit", Argument: "N", HasMinimum: true})
			return c
		}},
		{"missing cursor", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "limit", Argument: "N", HasMinimum: true, Minimum: 1})
			return c
		}},
		{"dangling relation", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "x", Requires: []string{"missing"}})
			return c
		}},
		{"repeat switch", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "label", Repeatable: true, CommaList: true})
			return c
		}},
		{"legacy selector", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "node", Argument: "ID"})
			return c
		}},
		{"grep records", func(c []Command) []Command {
			c[0].Flags = append(c[0].Flags, Flag{Name: "grep", Argument: "PATTERN"})
			return c
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validate(tt.change(Registry())); err == nil {
				t.Fatal("accepted invalid grammar")
			}
		})
	}
}

func TestFinalSurfaceHasNoRemovedCommands(t *testing.T) {
	cuts := []string{"sparkwing cluster", "sparkwing cloud", "sparkwing configure", "sparkwing serve", "sparkwing crons", "sparkwing runs consumer", "sparkwing runs bounce", "sparkwing runs reattach", "sparkwing debug rerun", "sparkwing debug run", "sparkwing debug release", "sparkwing dashboard restart", "sparkwing pipeline sparks resolve", "sparkwing pipeline sparks warmup", "sparkwing-runner agent", "sparkwing-runner runner", "sparkwing-runner wingd", "sparkwing wingd supervise"}
	for _, c := range Registry() {
		for _, cut := range cuts {
			if c.Path == cut || strings.HasPrefix(c.Path, cut+" ") {
				t.Errorf("removed command %s", c.Path)
			}
		}
	}
}

func TestLaunchCriticalSurfaces(t *testing.T) {
	commands := map[string]Command{}
	for _, c := range Registry() {
		commands[c.Path] = c
	}
	required := map[string][]string{
		"sparkwing run":               {"publish", "sw-cache-from", "sw-output", "sw-pause-before", "sw-no-memo", "sw-allow-risk"},
		"sparkwing pipeline trigger":  {"resume", "runner", "profile"},
		"sparkwing dashboard start":   {"stay"},
		"sparkwing dashboard install": {}, "sparkwing dashboard uninstall": {}, "sparkwing __tick": {},
		"sparkwing schedules pause": {"schedule"}, "sparkwing schedules resume": {"schedule"},
		"sparkwing runners add": {"repos", "reserved-for"}, "sparkwing pools allow": {"pool", "repo", "branch", "source", "route"},
		"sparkwing-runner start": {"config", "repos"}, "sparkwing-runner warm": {"github-actions", "poll"},
		"sparkwing-runner run-node": {"bootstrap-file", "generation"}, "sparkwing-controller admin freeze": {"team", "reason"},
	}
	for path, names := range required {
		c, ok := commands[path]
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		for _, n := range names {
			if !slices.ContainsFunc(c.Flags, func(f Flag) bool { return f.Name == n }) {
				t.Errorf("%s missing --%s", path, n)
			}
		}
	}
	if !commands["sparkwing __tick"].Hidden {
		t.Error("timer entrypoint must be hidden")
	}
	if !commands["sparkwing pipeline trigger"].PipelineOptionalOnResume {
		t.Error("resume must not need a new pipeline")
	}
	if slices.ContainsFunc(commands["sparkwing run"].Flags, func(f Flag) bool { return f.Name == "profile" }) {
		t.Error("local run accepts remote execution profile")
	}
	if slices.Contains(commands["sparkwing dashboard install"].Platforms, "windows") {
		t.Error("Windows install must be refused")
	}
	for _, c := range Registry() {
		if strings.HasPrefix(c.Path, "sparkwing operator ") && c.Authority != deploymentAdmin {
			t.Errorf("%s crosses authority boundary", c.Path)
		}
	}
}

func TestRegistryReturnsOwnedData(t *testing.T) {
	first := Registry()
	first[0].Path = "changed"
	globals := GlobalFlags()
	globals[0].Values[0] = "changed"
	if Registry()[0].Path == "changed" || GlobalFlags()[0].Values[0] == "changed" {
		t.Fatal("caller mutation escaped its registry")
	}
}

func TestRunOutputMatchesGlobalModes(t *testing.T) {
	var global, run []string
	for _, f := range GlobalFlags() {
		if f.Name == "output" {
			global = f.Values
		}
	}
	for _, c := range Registry() {
		if c.Path == "sparkwing run" {
			for _, f := range c.Flags {
				if f.Name == "sw-output" {
					run = f.Values
				}
			}
		}
	}
	if !slices.Equal(global, run) || !slices.Equal(global, []string{"pretty", "json", "plain"}) {
		t.Fatalf("output modes differ: global=%v run=%v", global, run)
	}
}

func TestForegroundWorkersAndHostReason(t *testing.T) {
	var workers Flag
	var reason bool
	for _, c := range Registry() {
		for _, f := range c.Flags {
			if c.Path == "sparkwing run" && f.Name == "sw-workers" {
				workers = f
			}
			if c.Path == "sparkwing __host-trigger" && f.Name == "standalone-reason" {
				reason = f.Argument == "REASON"
			}
		}
	}
	if workers.Argument != "N" || !workers.HasMinimum || workers.Minimum != 1 || !slices.Contains(workers.Conflicts, "sw-detached") {
		t.Fatalf("invalid workers contract: %+v", workers)
	}
	if !reason {
		t.Fatal("host-trigger lacks standalone reason")
	}
}
