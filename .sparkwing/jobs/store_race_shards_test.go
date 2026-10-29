package jobs

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestStoreRaceShardsCoverEveryListedNameOnce(t *testing.T) {
	names, err := storeRaceNames("TestZ\nExampleA\nFuzzSeed\nTestA\nTestB\nTestC\nTestD\nTestE\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("test names not sorted: %v", names)
	}
	shards, err := storeRaceShards(names)
	if err != nil {
		t.Fatal(err)
	}
	for i, shard := range shards {
		if len(shard) != 2 {
			t.Errorf("shard %d = %v, want two names", i+1, shard)
		}
	}
	if err := checkStoreRaceCoverage(names, shards); err != nil {
		t.Fatal(err)
	}

	dropped := slices.Clone(shards)
	dropped[0] = dropped[0][:1]
	if err := checkStoreRaceCoverage(names, dropped); err == nil || !strings.Contains(err.Error(), "no shard") {
		t.Fatalf("dropped test: %v, want coverage failure", err)
	}
	duplicated := slices.Clone(shards)
	duplicated[0] = append(slices.Clone(duplicated[0]), duplicated[1][0])
	if err := checkStoreRaceCoverage(names, duplicated); err == nil || !strings.Contains(err.Error(), "multiple shards") {
		t.Fatalf("duplicated test: %v, want disjointness failure", err)
	}
}

func TestStoreRaceListingRejectsAmbiguousNames(t *testing.T) {
	for _, listing := range []string{
		"TestA\nTestA\nTestB\nTestC\nTestD\n",
		"TestA\nTestB\nTestC\nTestD\nnoise\n",
		"TestA\nTestB\nTestC\n",
	} {
		if names, err := storeRaceNames(listing); err == nil {
			t.Errorf("listing %q accepted as %v", listing, names)
		}
	}
}

func TestStoreRacePatternAnchorsEveryTopLevelName(t *testing.T) {
	pattern := storeRacePattern([]string{"TestA", "ExampleB", "FuzzC"})
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
	result, err := storeRaceExec(t.Context(), home, "env").Capture()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(result.Stdout, "\n")
	for _, want := range []string{
		"SPARKWING_HOME=" + home,
		"GOWORK=off",
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
