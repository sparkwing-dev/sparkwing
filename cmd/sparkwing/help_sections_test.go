package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestRootHelpGroupsEveryTopLevelCommand(t *testing.T) {
	root := surfaceCommand("sparkwing")
	for _, sub := range visibleSubcommands(root) {
		if !slices.Contains(commandSectionOrder, sub.Section) {
			t.Errorf("top-level command %q has no root help section", sub.Name)
		}
	}
	var out bytes.Buffer
	PrintHelp(root, &out)
	text := out.String()
	local, cloud, admin := strings.Index(text, "\nLOCAL"), strings.Index(text, "\nCLOUD"), strings.Index(text, "\nADMIN AND HELP")
	if local < 0 || cloud < local || admin < cloud {
		t.Fatalf("root help sections out of order or missing:\n%s", text)
	}
	for _, tc := range []struct {
		name       string
		start, end int
	}{{"run", local, cloud}, {"secrets", local, cloud}, {"login", cloud, admin}, {"tokens", cloud, admin}, {"operator", admin, len(text)}, {"docs", admin, len(text)}} {
		if !strings.Contains(text[tc.start:tc.end], "\n  "+tc.name+" ") {
			t.Errorf("%s is not listed in its section", tc.name)
		}
	}
}

func TestRunHelpLeadsWithPlainLanguageAndKeepsInternalsForHelpAll(t *testing.T) {
	render := func(all bool) string {
		help := surfaceCommand("sparkwing run")
		help.ShowAll = all
		var out bytes.Buffer
		PrintHelp(help, &out)
		return out.String()
	}
	brief, full := render(false), render(true)
	for _, term := range []string{"sealed", "intake", "unadmitted"} {
		if strings.Contains(strings.ToLower(brief), term) {
			t.Errorf("run --help uses the internal term %q", term)
		}
		if !strings.Contains(strings.ToLower(full), term) {
			t.Errorf("run --help-all lost %q", term)
		}
	}
	for _, want := range []string{"on this machine", "after --", "sparkwing runs logs", "--help-all", "EXAMPLES"} {
		if !strings.Contains(brief, want) {
			t.Errorf("run --help is missing %q:\n%s", want, brief)
		}
	}
}

func TestEveryHelpExampleNamesARealCommand(t *testing.T) {
	for path, examples := range commandExamples {
		if !registered(path) {
			t.Errorf("examples for unregistered %q", path)
		}
		for _, example := range examples {
			if !strings.HasPrefix(example.Command, path+" ") && example.Command != path {
				t.Errorf("%s example %q runs another command", path, example.Command)
			}
			if _, err := clisurface.Parse("sparkwing", strings.Fields(example.Command)[1:]); err != nil {
				t.Errorf("%s example %q does not parse: %v", path, example.Command, err)
			}
		}
	}
	if len(commandExamples) < 20 {
		t.Errorf("examples cover %d commands, want at least 20", len(commandExamples))
	}
}

func TestCommonCommandsHaveDescriptionsAndExamples(t *testing.T) {
	paths := []string{"runs", "pipeline", "pipeline new", "pipeline list", "pipeline upgrade", "pipeline describe", "pipeline plan", "pipeline trigger", "secrets", "daemon", "runners", "login", "profiles", "cache", "docs", "examples"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			command := surfaceCommand("sparkwing " + path)
			if command.Description == "" || len(command.Examples) == 0 {
				t.Fatalf("missing description or examples for %s", path)
			}
		})
	}
	for _, surface := range clisurface.Registry() {
		if strings.HasPrefix(surface.Path, "sparkwing runs ") && surfaceHelp(surface).Description == "" {
			t.Errorf("missing description for %s", surface.Path)
		}
	}
}

func TestEveryLeafCommandHasPlainIntro(t *testing.T) {
	registry := clisurface.Registry()
	for _, surface := range registry {
		if surface.Path != "sparkwing" && !strings.HasPrefix(surface.Path, "sparkwing ") {
			continue
		}
		if slices.ContainsFunc(registry, func(other clisurface.Command) bool {
			return strings.HasPrefix(other.Path, surface.Path+" ")
		}) {
			continue
		}
		if strings.TrimSpace(commandIntros[surface.Path]) == "" && surface.Path != "sparkwing commands" {
			t.Errorf("missing plain intro for %s", surface.Path)
		}
	}
}
