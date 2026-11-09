package jobs

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestRaceShardsCoverEveryListedNameOnce(t *testing.T) {
	for _, shardCount := range []int{1, 2, storeRaceShardCount} {
		names, err := raceTestNames("TestZ\nExampleA\nFuzzSeed\nTestA\nTestB\nTestC\nTestD\nTestE\n", shardCount)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.IsSorted(names) {
			t.Fatalf("test names not sorted: %v", names)
		}
		shards, err := raceTestShards(names, shardCount)
		if err != nil {
			t.Fatal(err)
		}
		for i, shard := range shards {
			if len(shard) != len(names)/shardCount {
				t.Errorf("shard %d = %v, want %d names", i+1, shard, len(names)/shardCount)
			}
		}
		if err := checkRaceTestCoverage(names, shards, shardCount); err != nil {
			t.Fatal(err)
		}

		dropped := slices.Clone(shards)
		dropped[0] = dropped[0][:1]
		if err := checkRaceTestCoverage(names, dropped, shardCount); err == nil || !strings.Contains(err.Error(), "no shard") {
			t.Fatalf("dropped test: %v, want coverage failure", err)
		}
		duplicated := slices.Clone(shards)
		duplicateName := shards[0][0]
		if shardCount > 1 {
			duplicateName = shards[1][0]
		}
		duplicated[0] = append(slices.Clone(duplicated[0]), duplicateName)
		if err := checkRaceTestCoverage(names, duplicated, shardCount); err == nil || !strings.Contains(err.Error(), "multiple shards") {
			t.Fatalf("duplicated test: %v, want disjointness failure", err)
		}
	}
}

func TestStoreRaceListingRejectsAmbiguousNames(t *testing.T) {
	for _, listing := range []string{
		"TestA\nTestA\nTestB\nTestC\nTestD\n",
		"TestA\nTestB\nTestC\nTestD\nnoise\n",
		"TestA\nTestB\nTestC\n",
	} {
		if names, err := raceTestNames(listing, storeRaceShardCount); err == nil {
			t.Errorf("listing %q accepted as %v", listing, names)
		}
	}
}

func TestStoreRacePatternAnchorsEveryTopLevelName(t *testing.T) {
	pattern := raceTestPattern([]string{"TestA", "ExampleB", "FuzzC"})
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestA", "ExampleB", "FuzzC"} {
		if !re.MatchString(name) {
			t.Errorf("%q excluded by %q", name, pattern)
		}
	}
	for _, name := range []string{"TestAX", "OtherTestA", "TestA/child", "BenchmarkA"} {
		if re.MatchString(name) {
			t.Errorf("%q included by %q", name, pattern)
		}
	}
}

func TestStoreRaceExecIsolatesProductStateAndGoWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", "operator-home")
	t.Setenv("SPARKWING_AGENT_TOKEN", "operator-token")
	t.Setenv("GOWORK", "operator-workspace")
	result, err := raceExec(newTestContext(t), home, "env").Capture()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(result.Stdout, "\n")
	for _, want := range []string{
		"SPARKWING_HOME=" + home,
		"GOWORK=auto",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("suite environment lacks %q", want)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "SPARKWING_AGENT_TOKEN=") {
			t.Error("suite inherited the runner credential SPARKWING_AGENT_TOKEN")
		}
	}
}

func TestStoreRaceShardsRunTheShortClass(t *testing.T) {
	if args := storeRaceShardArgs([]string{"TestA"}); !slices.Contains(args, "-test.short") || !slices.Contains(args, "-test.timeout=80m") {
		t.Fatalf("store race shard args = %q, want the short class under the 80-minute bound", args)
	}
}

func TestRaceLeadTestsGetTheirOwnShardsFirst(t *testing.T) {
	names := []string{"TestA", "TestB", "TestC", "TestLong", "TestZ"}
	shards, err := raceLeadShards(names, []string{"TestLong"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"TestLong"}, {"TestA", "TestC"}, {"TestB", "TestZ"}}
	if !slices.EqualFunc(shards, want, slices.Equal[[]string]) {
		t.Fatalf("shards=%v, want %v", shards, want)
	}
	if _, err := raceLeadShards(names, []string{"TestRenamed"}, 2); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("missing lead test: %v, want refusal", err)
	}
	if _, err := raceLeadShards(names, []string{"TestA", "TestB", "TestC", "TestLong"}, 2); err == nil {
		t.Fatal("accepted fewer remaining names than round-robin shards")
	}
}

func TestRaceCostShardsBalanceLongestFirst(t *testing.T) {
	names := []string{"TestA", "TestB", "TestC", "TestD", "TestE", "TestNew"}
	costs := map[string]float64{"TestA": 10, "TestB": 6, "TestC": 5, "TestD": 4, "TestE": 1, "TestGone": 99}
	shards, err := raceCostShards(names, costs, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"TestA", "TestC", "TestE"}, {"TestB", "TestD", "TestNew"}}
	if !slices.EqualFunc(shards, want, slices.Equal[[]string]) {
		t.Fatalf("shards=%v, want %v", shards, want)
	}
	reversed := slices.Clone(names)
	slices.Reverse(reversed)
	again, err := raceCostShards(reversed, costs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(again, want, slices.Equal[[]string]) {
		t.Fatalf("listing order changed the assignment: %v", again)
	}
	if _, err := raceCostShards(names[:1], costs, 2); err == nil {
		t.Fatal("accepted fewer names than shards")
	}
}

func TestRaceCostShardsFillEveryShardWhenCostsAreZero(t *testing.T) {
	names := []string{"TestA", "TestB", "TestC", "TestD"}
	costs := map[string]float64{"TestA": 0, "TestB": 0, "TestC": 0, "TestD": 0}
	shards, err := raceCostShards(names, costs, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i, shard := range shards {
		if len(shard) != 1 {
			t.Fatalf("shard %d = %v, want one name", i+1, shard)
		}
	}
}

func TestRaceStorePartsTogetherRunEveryShardOnce(t *testing.T) {
	names := make([]string, 0, 64)
	for i := range 64 {
		names = append(names, fmt.Sprintf("Test%02d", i))
	}
	for count := 1; count <= maxRaceStoreParts; count++ {
		all, err := raceCostShards(names, map[string]float64{"Test07": 30}, count*storeRaceShardCount)
		if err != nil {
			t.Fatal(err)
		}
		var union [][]string
		for index := 1; index <= count; index++ {
			part := raceStorePart{index: index, count: count}
			mine := part.shards(all)
			if len(mine) != storeRaceShardCount {
				t.Fatalf("part %d/%d runs %d shards, want %d", index, count, len(mine), storeRaceShardCount)
			}
			union = append(union, mine...)
		}
		if err := checkRaceTestCoverage(names, union, len(all)); err != nil {
			t.Fatalf("%d parts: %v", count, err)
		}
	}
}

func TestParseRaceStorePart(t *testing.T) {
	if part, err := parseRaceStorePart(""); err != nil || part != (raceStorePart{index: 1, count: 1}) {
		t.Fatalf("default part = %v, %v; want every shard", part, err)
	}
	if part, err := parseRaceStorePart("none"); err != nil || part.count != 0 {
		t.Fatalf("none = %v, %v; want no shard", part, err)
	}
	if part, err := parseRaceStorePart("2/3"); err != nil || part != (raceStorePart{index: 2, count: 3}) {
		t.Fatalf("2/3 = %v, %v", part, err)
	}
	for _, bad := range []string{"0/3", "4/3", "3", "a/3", "1/0", "1/9", "1/3/4", " 1/3"} {
		if part, err := parseRaceStorePart(bad); err == nil {
			t.Errorf("accepted %q as %v", bad, part)
		}
	}
}

func TestStoreRaceCostTableParses(t *testing.T) {
	costs, err := parseRaceCosts(storeRaceCostTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) < 1000 {
		t.Fatalf("cost table has %d names; regenerate it from a full race run", len(costs))
	}
	for _, bad := range []string{"", "# only a comment\n", "1.5\n", "x TestA\n", "-1 TestA\n", "1 TestA/sub\n", "1 TestA\n2 TestA\n"} {
		if _, err := parseRaceCosts(bad); err == nil {
			t.Errorf("accepted cost table %q", bad)
		}
	}
}
