package acceptance

import "slices"

// Targets enumerates required native execution cells rather than relying on a Linux-only default.
// Local targets use the local controller unless Standalone selects in-process CI execution.
func Targets() []Target {
	return []Target{
		{"local-go-linux", Local, "go", "linux", "sqlite", "filesystem", Native, false},
		{"local-go-darwin", Local, "go", "darwin", "sqlite", "filesystem", Native, false},
		{"local-go-windows", Local, "go", "windows", "sqlite", "filesystem", Native, false},
		{"local-ts-linux", Local, "typescript", "linux", "sqlite", "filesystem", Native, false},
		{"local-ts-darwin", Local, "typescript", "darwin", "sqlite", "filesystem", Native, false},
		{"local-ts-windows", Local, "typescript", "windows", "sqlite", "filesystem", Native, false},
		{"standalone-go-linux", Local, "go", "linux", "sqlite", "filesystem", Native, true},
		{"standalone-ts-linux", Local, "typescript", "linux", "sqlite", "filesystem", Native, true},
		{"triggered-go-linux", LocalTriggered, "go", "linux", "sqlite", "filesystem", Native, false},
		{"triggered-go-darwin", LocalTriggered, "go", "darwin", "sqlite", "filesystem", Native, false},
		{"triggered-go-windows", LocalTriggered, "go", "windows", "sqlite", "filesystem", Native, false},
		{"triggered-ts-linux", LocalTriggered, "typescript", "linux", "sqlite", "filesystem", Native, false},
		{"triggered-ts-darwin", LocalTriggered, "typescript", "darwin", "sqlite", "filesystem", Native, false},
		{"triggered-ts-windows", LocalTriggered, "typescript", "windows", "sqlite", "filesystem", Native, false},
		{"runner-go-linux", RunnerHosted, "go", "linux", "postgres", "s3", Native, false},
		{"runner-go-darwin", RunnerHosted, "go", "darwin", "postgres", "s3", Native, false},
		{"runner-go-windows", RunnerHosted, "go", "windows", "postgres", "s3", Native, false},
		{"runner-ts-linux", RunnerHosted, "typescript", "linux", "postgres", "s3", Native, false},
		{"runner-ts-darwin", RunnerHosted, "typescript", "darwin", "postgres", "s3", Native, false},
		{"runner-ts-windows", RunnerHosted, "typescript", "windows", "postgres", "s3", Native, false},
		{"launcher-go-s3", Launcher, "go", "linux", "postgres", "s3", Cluster, false},
		{"launcher-ts-s3", Launcher, "typescript", "linux", "postgres", "s3", Cluster, false},
		{"launcher-go-filesystem", Launcher, "go", "linux", "sqlite", "filesystem", Cluster, false},
		{"launcher-ts-filesystem", Launcher, "typescript", "linux", "sqlite", "filesystem", Cluster, false},
		{"here-go-linux", Here, "go", "linux", "postgres", "s3", Native, false},
		{"here-go-darwin", Here, "go", "darwin", "postgres", "s3", Native, false},
		{"here-go-windows", Here, "go", "windows", "postgres", "s3", Native, false},
		{"here-ts-linux", Here, "typescript", "linux", "postgres", "s3", Native, false},
		{"here-ts-darwin", Here, "typescript", "darwin", "postgres", "s3", Native, false},
		{"here-ts-windows", Here, "typescript", "windows", "postgres", "s3", Native, false},
		{"actions-go-linux", GitHubActions, "go", "linux", "postgres", "s3", Hosted, false},
		{"actions-go-windows", GitHubActions, "go", "windows", "postgres", "s3", Hosted, false},
		{"actions-ts-linux", GitHubActions, "typescript", "linux", "postgres", "s3", Hosted, false},
		{"actions-ts-windows", GitHubActions, "typescript", "windows", "postgres", "s3", Hosted, false},
	}
}

// Applies selects the declared placements and optional OS, SDK and controller/standalone modes.
// Empty restriction lists require every supported variant of the selected placements.
func Applies(scenario Scenario, target Target) bool {
	mode := "controller"
	if target.Standalone {
		mode = "standalone"
	}
	return slices.Contains(scenario.Placements, target.Placement) &&
		(len(scenario.OS) == 0 || slices.Contains(scenario.OS, target.OS)) &&
		(len(scenario.SDKs) == 0 || slices.Contains(scenario.SDKs, target.SDK)) &&
		(len(scenario.Modes) == 0 || slices.Contains(scenario.Modes, mode))
}

// Selections expands scenarios across all applicable native variants and execution modes.
func Selections() []Selection {
	var selections []Selection
	for _, scenario := range Scenarios() {
		for _, target := range Targets() {
			if Applies(scenario, target) {
				selections = append(selections, Selection{ScenarioID: scenario.ID, TargetID: target.ID})
			}
		}
	}
	return selections
}
