package acceptance_test

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

type invariantRequirement struct {
	references                  []string
	scenario                    string
	placements, os, sdks, modes []string
	excerpt                     string
}

func readInvariantRequirements(t *testing.T) []invariantRequirement {
	t.Helper()
	body, err := os.ReadFile("../../design/rewrite/contracts/t1-invariants.md")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseInvariantRequirements(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func parseInvariantRequirements(body string) ([]invariantRequirement, error) {
	const begin, end = "<!-- invariants:begin -->", "<!-- invariants:end -->"
	if strings.Count(body, begin) != 1 || strings.Count(body, end) != 1 {
		return nil, fmt.Errorf("require one invariant table")
	}
	_, table, _ := strings.Cut(body, begin)
	table, _, found := strings.Cut(table, end)
	if !found {
		return nil, fmt.Errorf("invariant delimiters out of order")
	}
	lines := strings.Split(strings.TrimSpace(table), "\n")
	if len(lines) < 3 || lines[0] != "| References | Scenario | Placements | OS | SDK | Modes | Assertion excerpt |" || lines[1] != "|---|---|---|---|---|---|---|" {
		return nil, fmt.Errorf("malformed invariant table header")
	}
	var rows []invariantRequirement
	seen := map[string]bool{}
	for i, line := range lines[2:] {
		cells := strings.Split(line, "|")
		if len(cells) != 9 || cells[0] != "" || cells[8] != "" {
			return nil, fmt.Errorf("malformed invariant row %d", i+1)
		}
		for c := 1; c <= 7; c++ {
			cells[c] = strings.TrimSpace(cells[c])
			if cells[c] == "" {
				return nil, fmt.Errorf("empty invariant cell at row %d", i+1)
			}
		}
		key := cells[2] + ":" + cells[7]
		if seen[key] {
			return nil, fmt.Errorf("duplicate invariant assertion %s", key)
		}
		seen[key] = true
		row := invariantRequirement{strings.Split(cells[1], ", "), cells[2], strings.Split(cells[3], ", "), strings.Split(cells[4], ", "), strings.Split(cells[5], ", "), strings.Split(cells[6], ", "), cells[7]}
		for _, dimension := range []struct{ values, allowed []string }{
			{row.placements, []string{"local", "local_triggered", "runner_hosted", "launcher", "here", "github_actions"}},
			{row.os, []string{"linux", "darwin", "windows"}},
			{row.sdks, []string{"go", "typescript"}},
			{row.modes, []string{"controller", "standalone"}},
		} {
			seenValues := map[string]bool{}
			for _, value := range dimension.values {
				if seenValues[value] || (value == "all" && len(dimension.values) != 1) || (value != "all" && !slices.Contains(dimension.allowed, value)) {
					return nil, fmt.Errorf("invalid dimension %q at row %d", value, i+1)
				}
				seenValues[value] = true
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func checkInvariantCoverage(rows []invariantRequirement, scenarios []acceptance.Scenario, targets []acceptance.Target, selections []acceptance.Selection) error {
	expected := requiredTargets()
	actual := map[string]acceptance.Target{}
	for _, target := range targets {
		if _, ok := actual[target.ID]; ok {
			return fmt.Errorf("duplicate target %s", target.ID)
		}
		actual[target.ID] = target
	}
	for _, target := range expected {
		if actual[target.ID] != target {
			return fmt.Errorf("required target missing or changed: %s", target.ID)
		}
		delete(actual, target.ID)
	}
	if len(actual) != 0 {
		return fmt.Errorf("unexpected targets: %v", actual)
	}
	catalog := map[string]acceptance.Scenario{}
	for _, scenario := range scenarios {
		catalog[scenario.ID] = scenario
	}
	selected := map[acceptance.Selection]bool{}
	for _, selection := range selections {
		selected[selection] = true
	}
	references, covered := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		scenario, ok := catalog[row.scenario]
		if !ok {
			return fmt.Errorf("unknown invariant scenario %s", row.scenario)
		}
		matches := 0
		for _, assertion := range scenario.Assertions {
			if strings.Contains(assertion, row.excerpt) {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("invariant excerpt for %s matches %d assertions: %q", row.scenario, matches, row.excerpt)
		}
		for _, reference := range row.references {
			if !slices.Contains(scenario.Sections, reference) {
				return fmt.Errorf("%s omits invariant reference %s", row.scenario, reference)
			}
			references[reference] = true
		}
		cells := 0
		for _, target := range expected {
			mode := "controller"
			if target.Standalone {
				mode = "standalone"
			}
			if !requiredDimension(row.placements, string(target.Placement)) || !requiredDimension(row.os, target.OS) || !requiredDimension(row.sdks, target.SDK) || !requiredDimension(row.modes, mode) {
				continue
			}
			cells++
			selection := acceptance.Selection{ScenarioID: row.scenario, TargetID: target.ID}
			if !acceptance.Applies(scenario, target) || !selected[selection] {
				return fmt.Errorf("required execution cell missing: %s/%s", row.scenario, target.ID)
			}
		}
		if cells == 0 {
			return fmt.Errorf("invariant %s requires no execution cells", row.scenario)
		}
		covered[row.scenario] = true
	}
	for _, scenario := range scenarios {
		if !covered[scenario.ID] {
			return fmt.Errorf("scenario lacks independent requirements: %s", scenario.ID)
		}
	}
	for i := 1; i <= 73; i++ {
		if !references["A"+strconv.Itoa(i)] {
			return fmt.Errorf("unmapped invariant A%d", i)
		}
	}
	for i := 101; i <= 104; i++ {
		if !references["decision "+strconv.Itoa(i)] {
			return fmt.Errorf("unmapped decision %d", i)
		}
	}
	return nil
}

func requiredDimension(values []string, value string) bool {
	return slices.Contains(values, "all") || slices.Contains(values, value)
}

func TestInvariantCoverage(t *testing.T) {
	if err := checkInvariantCoverage(readInvariantRequirements(t), acceptance.Scenarios(), acceptance.Targets(), acceptance.Selections()); err != nil {
		t.Fatal(err)
	}
}

func TestInvariantCoverageRejectsLostEvidence(t *testing.T) {
	rows := readInvariantRequirements(t)
	if err := checkInvariantCoverage(rows, acceptance.Scenarios(), acceptance.Targets(), acceptance.Selections()); err != nil {
		t.Fatalf("invalid mutation baseline: %v", err)
	}
	for _, mutation := range []string{"remove Windows TypeScript target", "narrow scenario OS", "delete cited assertion", "remove selection"} {
		t.Run(mutation, func(t *testing.T) {
			scenarios, targets, selections := acceptance.Scenarios(), acceptance.Targets(), acceptance.Selections()
			switch mutation {
			case "remove Windows TypeScript target":
				targets = slices.DeleteFunc(targets, func(target acceptance.Target) bool { return target.ID == "local-ts-windows" })
			case "narrow scenario OS":
				scenarios[0].OS = []string{"linux"}
			case "delete cited assertion":
				scenarios[0].Assertions = scenarios[0].Assertions[1:]
			case "remove selection":
				selections = slices.DeleteFunc(selections, func(selection acceptance.Selection) bool {
					return selection.ScenarioID == scenarios[0].ID && selection.TargetID == "local-ts-windows"
				})
			}
			if err := checkInvariantCoverage(rows, scenarios, targets, selections); err == nil {
				t.Fatal("lost evidence passed coverage guard")
			}
		})
	}
}

func requiredTargets() []acceptance.Target {
	return []acceptance.Target{
		{ID: "local-go-linux", Placement: acceptance.Local, SDK: "go", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "local-go-darwin", Placement: acceptance.Local, SDK: "go", OS: "darwin", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "local-go-windows", Placement: acceptance.Local, SDK: "go", OS: "windows", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "local-ts-linux", Placement: acceptance.Local, SDK: "typescript", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "local-ts-darwin", Placement: acceptance.Local, SDK: "typescript", OS: "darwin", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "local-ts-windows", Placement: acceptance.Local, SDK: "typescript", OS: "windows", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "standalone-go-linux", Placement: acceptance.Local, SDK: "go", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: true},
		{ID: "standalone-ts-linux", Placement: acceptance.Local, SDK: "typescript", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: true},
		{ID: "triggered-go-linux", Placement: acceptance.LocalTriggered, SDK: "go", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "triggered-go-darwin", Placement: acceptance.LocalTriggered, SDK: "go", OS: "darwin", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "triggered-go-windows", Placement: acceptance.LocalTriggered, SDK: "go", OS: "windows", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "triggered-ts-linux", Placement: acceptance.LocalTriggered, SDK: "typescript", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "triggered-ts-darwin", Placement: acceptance.LocalTriggered, SDK: "typescript", OS: "darwin", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "triggered-ts-windows", Placement: acceptance.LocalTriggered, SDK: "typescript", OS: "windows", State: "sqlite", Data: "filesystem", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-go-linux", Placement: acceptance.RunnerHosted, SDK: "go", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-go-darwin", Placement: acceptance.RunnerHosted, SDK: "go", OS: "darwin", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-go-windows", Placement: acceptance.RunnerHosted, SDK: "go", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-ts-linux", Placement: acceptance.RunnerHosted, SDK: "typescript", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-ts-darwin", Placement: acceptance.RunnerHosted, SDK: "typescript", OS: "darwin", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "runner-ts-windows", Placement: acceptance.RunnerHosted, SDK: "typescript", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "launcher-go-s3", Placement: acceptance.Launcher, SDK: "go", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Cluster, Standalone: false},
		{ID: "launcher-ts-s3", Placement: acceptance.Launcher, SDK: "typescript", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Cluster, Standalone: false},
		{ID: "launcher-go-filesystem", Placement: acceptance.Launcher, SDK: "go", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Cluster, Standalone: false},
		{ID: "launcher-ts-filesystem", Placement: acceptance.Launcher, SDK: "typescript", OS: "linux", State: "sqlite", Data: "filesystem", Lane: acceptance.Cluster, Standalone: false},
		{ID: "here-go-linux", Placement: acceptance.Here, SDK: "go", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "here-go-darwin", Placement: acceptance.Here, SDK: "go", OS: "darwin", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "here-go-windows", Placement: acceptance.Here, SDK: "go", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "here-ts-linux", Placement: acceptance.Here, SDK: "typescript", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "here-ts-darwin", Placement: acceptance.Here, SDK: "typescript", OS: "darwin", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "here-ts-windows", Placement: acceptance.Here, SDK: "typescript", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Native, Standalone: false},
		{ID: "actions-go-linux", Placement: acceptance.GitHubActions, SDK: "go", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Hosted, Standalone: false},
		{ID: "actions-go-windows", Placement: acceptance.GitHubActions, SDK: "go", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Hosted, Standalone: false},
		{ID: "actions-ts-linux", Placement: acceptance.GitHubActions, SDK: "typescript", OS: "linux", State: "postgres", Data: "s3", Lane: acceptance.Hosted, Standalone: false},
		{ID: "actions-ts-windows", Placement: acceptance.GitHubActions, SDK: "typescript", OS: "windows", State: "postgres", Data: "s3", Lane: acceptance.Hosted, Standalone: false},
	}
}

func TestInvariantRowsRejectMalformedInput(t *testing.T) {
	body, err := os.ReadFile("../../design/rewrite/contracts/t1-invariants.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, test := range []struct{ name, old, replacement string }{
		{"missing column", "| References | Scenario |", "| References |"},
		{"missing delimiter", "<!-- invariants:end -->", ""},
		{"invalid dimension", "| all |", "| linx |"},
		{"empty dimension", "| all |", "|  |"},
		{"unparsed row", "<!-- invariants:end -->", "malformed row\n<!-- invariants:end -->"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := strings.Replace(text, test.old, test.replacement, 1)
			if changed == text {
				t.Fatal("mutation did not change fixture")
			}
			if _, err := parseInvariantRequirements(changed); err == nil {
				t.Fatal("malformed invariant table accepted")
			}
		})
	}
}
