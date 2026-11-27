package jobs

import (
	"regexp"
	"strconv"
	"testing"
)

var goParallelismFlag = regexp.MustCompile(`(?:^| )go (?:-C \S+ )?(?:test|vet|build)(?: -json)? -p ([0-9]+)`)

func commandParallelism(t *testing.T, command string) int {
	t.Helper()
	match := goParallelismFlag.FindStringSubmatch(command)
	if match == nil {
		t.Fatalf("no -p in %q", command)
	}
	n, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGateCoreRequestsEqualTheParallelismTheirCommandsRun(t *testing.T) {
	plans := []affectedTestPlan{
		{module: ".", packages: []string{storeTestPackage, "./internal/controller", "./cmd/sparkwing"}},
		{module: "sparkwing", packages: []string{"./protocol"}},
		{module: ".sparkwing", packages: []string{"./jobs"}},
	}
	for _, h := range []hostShape{{cpus: 1}, {cpus: 4}, {cpus: 4, singleTenant: true}, {cpus: 8}, {cpus: 16}, {cpus: 16, singleTenant: true}, {cpus: 32}} {
		gate := goStepParallelism(h)
		if got := commandParallelism(t, testGoCommand(h)); got != gate {
			t.Errorf("%+v: gate declares %d cores but its full test command runs -p %d", h, gate, got)
		}
		if got := commandParallelism(t, boundedGoCommand(h, "vet", "./...")); got != gate {
			t.Errorf("%+v: gate declares %d cores but vet runs -p %d", h, gate, got)
		}
		widest := 0
		for _, group := range affectedTestGroups(plans, gate) {
			sum := 0
			for _, command := range group {
				sum += commandParallelism(t, command.command)
			}
			widest = max(widest, sum)
		}
		if widest != gate {
			t.Errorf("%+v: gate declares %d cores but its widest concurrent affected-test group runs -p %d in total", h, gate, widest)
		}
		race := raceJobCores(h, gateAffected)
		if got := commandParallelism(t, raceGoCommand(h, "-race -short ./...")); got != race {
			t.Errorf("%+v: race-touched declares %d cores but its command runs -p %d", h, race, got)
		}
		full := raceJobCores(h, gateFull)
		if workers := gateRaceParallelism(h) + gateRaceExtraWorkers(h); full != max(race, workers) {
			t.Errorf("%+v: full race-touched declares %d cores but runs one -p %d command, then %d shard workers", h, full, race, workers)
		}
	}
}
