package acceptance_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func TestCatalog(t *testing.T) {
	groups := map[string]bool{
		"lifecycle": false, "recovery": false, "placement": false,
		"credentials": false, "storage": false, "protocol": false,
		"environment": false, "surface": false, "billing": false,
	}
	fixtures := make(map[string]bool)
	for _, fixture := range acceptance.Fixtures() {
		if fixture.ID == "" || fixtures[fixture.ID] || fixture.Setup == "" || fixture.Oracle == "" {
			t.Fatalf("invalid or duplicate fixture %q", fixture.ID)
		}
		fixtures[fixture.ID] = true
	}
	targets := make(map[string]acceptance.Target)
	for _, target := range acceptance.Targets() {
		if _, exists := targets[target.ID]; exists || target.ID == "" {
			t.Fatalf("invalid or duplicate target %q", target.ID)
		}
		if target.SDK == "" || target.OS == "" || target.State == "" || target.Data == "" || target.Lane == "" {
			t.Fatalf("target %q omits a runtime dimension", target.ID)
		}
		targets[target.ID] = target
	}
	scenarios := make(map[string]acceptance.Scenario)
	decisions := make(map[string]bool)
	for _, scenario := range acceptance.Scenarios() {
		if _, exists := scenarios[scenario.ID]; exists || scenario.ID == "" {
			t.Fatalf("invalid or duplicate scenario %q", scenario.ID)
		}
		if _, exists := groups[scenario.Group]; !exists {
			t.Fatalf("scenario %q has no skeleton group %q", scenario.ID, scenario.Group)
		}
		groups[scenario.Group] = true
		if len(scenario.Sections) == 0 || len(scenario.Requires) == 0 || len(scenario.Placements) == 0 ||
			len(scenario.Fixtures) == 0 || len(scenario.Steps) == 0 || len(scenario.Assertions) == 0 {
			t.Fatalf("scenario %q omits its contract or oracle", scenario.ID)
		}
		for _, area := range scenario.Requires {
			switch area {
			case acceptance.C1, acceptance.C2, acceptance.C3, acceptance.C4:
			default:
				t.Fatalf("scenario %q names unknown area %q", scenario.ID, area)
			}
		}
		for _, fixture := range scenario.Fixtures {
			if !fixtures[fixture] {
				t.Fatalf("scenario %q names unknown fixture %q", scenario.ID, fixture)
			}
		}
		for _, mode := range scenario.Modes {
			if mode != "controller" && mode != "standalone" {
				t.Fatalf("scenario %q names unknown execution mode %q", scenario.ID, mode)
			}
		}
		for _, reference := range scenario.Sections {
			decisions[reference] = true
		}
		scenarios[scenario.ID] = scenario
	}
	seen := make(map[acceptance.Selection]bool)
	covered := make(map[string]bool)
	for _, selection := range acceptance.Selections() {
		if seen[selection] {
			t.Fatalf("duplicate execution cell %+v", selection)
		}
		seen[selection] = true
		scenario, exists := scenarios[selection.ScenarioID]
		if !exists {
			t.Fatalf("selection names unknown scenario %q", selection.ScenarioID)
		}
		target, exists := targets[selection.TargetID]
		if !exists || !acceptance.Applies(scenario, target) {
			t.Fatalf("selection %+v has no applicable target", selection)
		}
		covered[selection.ScenarioID] = true
	}
	for id := range scenarios {
		if !covered[id] {
			t.Errorf("scenario %q has no execution cell", id)
		}
	}
	for group, present := range groups {
		if !present {
			t.Errorf("skeleton group %q has no scenarios", group)
		}
	}
	for number := 1; number <= 73; number++ {
		id := "A" + strconv.Itoa(number)
		if !decisions[id] {
			t.Errorf("frozen architecture decision %s has no catalog reference", id)
		}
	}
	t.Logf("%d scenarios, %d targets, %d cells, %d fixtures; product scenarios remain skipped",
		len(scenarios), len(targets), len(seen), len(fixtures))
}

func TestCatalogDocumentation(t *testing.T) {
	body, err := os.ReadFile("../../design/rewrite/contracts/t1-scenarios.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string][]string)
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 10 {
			t.Fatalf("malformed scenario row %q", line)
		}
		id := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if _, exists := rows[id]; exists {
			t.Fatalf("duplicate documented scenario %q", id)
		}
		for _, cell := range cells[2:9] {
			rows[id] = append(rows[id], strings.TrimSpace(cell))
		}
	}
	for _, scenario := range acceptance.Scenarios() {
		row, exists := rows[scenario.ID]
		if !exists {
			t.Errorf("scenario %q is missing from the contract document", scenario.ID)
			continue
		}
		want := []string{
			strings.Join(scenario.Sections, ", "), joined(scenario.Requires), joined(scenario.Placements),
			"all", "all", "all", strings.Join(scenario.Fixtures, ", "),
		}
		if len(scenario.OS) != 0 {
			want[3] = strings.Join(scenario.OS, ", ")
		}
		if len(scenario.SDKs) != 0 {
			want[4] = strings.Join(scenario.SDKs, ", ")
		}
		if len(scenario.Modes) != 0 {
			want[5] = strings.Join(scenario.Modes, ", ")
		}
		for column := range want {
			if row[column] != want[column] {
				t.Errorf("scenario %q column %d documents %q, catalog requires %q",
					scenario.ID, column+2, row[column], want[column])
			}
		}
		delete(rows, scenario.ID)
	}
	for id := range rows {
		t.Errorf("document names unknown scenario %q", id)
	}
}

func joined[T ~string](values []T) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = string(value)
	}
	return strings.Join(parts, ", ")
}

func TestPortDestinations(t *testing.T) {
	scenarios := make(map[string]acceptance.Scenario)
	for _, scenario := range acceptance.Scenarios() {
		scenarios[scenario.ID] = scenario
	}
	body, err := os.ReadFile("../../design/rewrite/contracts/t1-port-plan.md")
	if err != nil {
		t.Fatal(err)
	}
	destination := regexp.MustCompile("`(lifecycle|recovery|placement|credentials|storage|protocol|environment|surface|billing)_test\\.go:(Test[A-Za-z]+)(?:/([a-z0-9-]+))?")
	matches := destination.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		t.Fatal("port plan contains no acceptance destinations")
	}
	for _, match := range matches {
		scenario, exists := scenarios[match[3]]
		if !exists || scenario.Group != match[1] {
			t.Errorf("port destination %q has no matching scenario", match[0])
		}
	}

	body, err = os.ReadFile("../../design/rewrite/contracts/t1-credential-routes.md")
	if err != nil {
		t.Fatal(err)
	}
	routeRow := regexp.MustCompile("(?m)^\\| `((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) [^`]+)` \\| ([a-z]+) \\| [^\\n]+? \\| `([^`]+)` \\|")
	routes := make(map[string]bool)
	for _, match := range routeRow.FindAllStringSubmatch(string(body), -1) {
		if routes[match[1]] {
			t.Errorf("duplicate credential source route %q", match[1])
		}
		routes[match[1]] = true
		if _, exists := scenarios[match[3]]; !exists {
			t.Errorf("credential route %q maps to unknown scenario %q", match[1], match[3])
		}
		switch match[2] {
		case "keep", "rename", "delete", "unresolved":
		default:
			t.Errorf("credential route %q has unknown disposition %q", match[1], match[2])
		}
	}
	if len(routes) != 229 {
		t.Errorf("credential port covers %d distinct source routes, want 229", len(routes))
	}
}
