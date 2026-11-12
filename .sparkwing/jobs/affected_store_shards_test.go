package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func TestAffectedStoreShardsRunEveryBodyAndKeepCriticalAfterFailure(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26\n")
	markers := filepath.Join(t.TempDir(), "markers")
	if err := os.Mkdir(markers, 0o700); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf(`func recordNamed(name string) error {
	f, err := os.OpenFile(filepath.Join(%q, strings.ReplaceAll(name, "/", "_")), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil { return fmt.Errorf("record body: %%w", err) }
	defer f.Close()
	return json.NewEncoder(f).Encode(struct { PID int; Short bool }{os.Getpid(), testing.Short()})
}
func record(t *testing.T) {
	t.Helper()
	if err := recordNamed(t.Name()); err != nil { t.Fatal(err) }
}
`, markers)
	imports := "import (\"encoding/json\"; \"fmt\"; \"os\"; \"path/filepath\"; \"strings\"; \"testing\")\n"
	writeGoFile(t, filepath.Join(root, "internal/store/store_test.go"), "package store\n"+imports+record+`
func TestCShard(t *testing.T) { record(t); t.Fatal("store shard body failed") }
func TestMShard(t *testing.T) { record(t) }
func TestAShard(t *testing.T) { record(t) }
func Test(t *testing.T) { record(t) }
func Test9(t *testing.T) { record(t) }
func TestΩ(t *testing.T) { record(t) }
func Fuzz(f *testing.F) { f.Add("seed"); f.Fuzz(func(t *testing.T, _ string) { record(t) }) }
func Example() {
	if err := recordNamed("Example"); err != nil { panic(err) }
	fmt.Println("ready")
	// Output: ready
}
`+criticalTestMarker+`
func TestZCritical(t *testing.T) { if testing.Short() { t.Skip("full critical only") }; record(t) }
`)
	writeGoFile(t, filepath.Join(root, "other/other_test.go"), "package other\n"+imports+record+"func TestOtherPackage(t *testing.T) { record(t) }\n")
	gitCommitAll(t, root, "store shard fixture")
	err := runAffectedTestFiles(newTestContext(t), []string{"internal/store/store_test.go", "other/other_test.go"}, "store shard fixture", hostShape{cpus: 16}, false)
	if err == nil || !strings.Contains(err.Error(), "store shard body failed") {
		t.Fatalf("store failure was lost: %v", err)
	}
	pids := map[int]bool{}
	for _, name := range []string{"TestCShard", "TestMShard", "TestAShard", "Test", "Test9", "TestΩ", "Fuzz_seed#0", "Example", "TestZCritical", "TestOtherPackage"} {
		data, err := os.ReadFile(filepath.Join(markers, name))
		if err != nil {
			t.Fatalf("%s body was cancelled or omitted: %v", name, err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s body ran %d times, want once: %s", name, len(lines), data)
		}
		var result struct {
			PID   int
			Short bool
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Short != (name != "TestZCritical") {
			t.Fatalf("%s short = %v", name, result.Short)
		}
		if name == "TestCShard" || name == "TestMShard" || name == "TestAShard" {
			pids[result.PID] = true
		}
	}
	if len(pids) != 3 {
		t.Fatalf("store short bodies used %d process(es), want three shards", len(pids))
	}
}

func TestStoreShortPatternsCoverEveryIdentifierInitialExactlyOnce(t *testing.T) {
	var patterns []*regexp.Regexp
	for _, pattern := range storeShortTestPatterns() {
		patterns = append(patterns, regexp.MustCompile(pattern))
	}
	check := func(name string, want int) {
		t.Helper()
		matches := 0
		for _, pattern := range patterns {
			if pattern.MatchString(name) {
				matches++
			}
		}
		if matches != want {
			t.Fatalf("%q belongs to %d shard(s), want %d", name, matches, want)
		}
	}
	for _, prefix := range []string{"Test", "Fuzz", "Example"} {
		check(prefix, 1)
		for initial := rune(0); initial <= unicode.MaxRune; initial++ {
			if initial == '_' || unicode.IsLetter(initial) || unicode.IsDigit(initial) {
				check(prefix+string(initial)+"Tail9", 1)
			}
		}
	}
	for _, name := range []string{"", "BenchmarkStore", "helper"} {
		check(name, 0)
	}
}

func TestAffectedStoreShardsStayWithinTheModuleCommandAllowance(t *testing.T) {
	const store = "github.com/sparkwing-dev/sparkwing/internal/store"
	const other = "github.com/sparkwing-dev/sparkwing/other"
	for _, tc := range []struct {
		cpus, allowance, shortCommands int
		others                         bool
	}{
		{3, 1, 1, false},
		{5, 2, 1, false},
		{7, 3, 3, false},
		{9, 4, 3, false},
		{16, 7, 3, false},
		{3, 1, 1, true},
		{5, 2, 1, true},
		{7, 3, 1, true},
		{9, 4, 4, true},
		{16, 7, 4, true},
	} {
		t.Run(fmt.Sprintf("allowance-%d/others-%v", tc.allowance, tc.others), func(t *testing.T) {
			packages := []string{store}
			if tc.others {
				packages = append(packages, other)
			}
			p := affectedTestPlan{module: ".", packages: packages, critical: map[string][]string{store: {"TestZCritical"}}}
			groups := affectedTestGroups([]affectedTestPlan{p}, goStepParallelism(hostShape{cpus: tc.cpus}))
			if len(groups) != 2 || len(groups[0]) != tc.shortCommands || len(groups[1]) != 1 {
				t.Fatalf("groups = %q, want %d short commands then one critical command", groups, tc.shortCommands)
			}
			width, storeCommands := 0, 0
			for _, planned := range groups[0] {
				command := planned.command
				var gomaxprocs, parallelism int
				if _, err := fmt.Sscanf(command, "GOMAXPROCS=%d go test -p %d", &gomaxprocs, &parallelism); err != nil {
					t.Fatal(err)
				}
				if gomaxprocs != parallelism || !strings.Contains(command, "-short -timeout 25m") {
					t.Fatalf("short command changed its Go limits or test class: %s", command)
				}
				width += parallelism
				if strings.Contains(command, shellQuote(store)) {
					storeCommands++
					if tc.shortCommands > 1 && (parallelism != 1 || strings.Contains(command, shellQuote(other))) {
						t.Fatalf("store shard shares its command or uses more than one core: %s", command)
					}
				}
			}
			wantStoreCommands := 1
			if tc.shortCommands > 1 {
				wantStoreCommands = 3
			}
			wantWidth := tc.allowance
			if !tc.others && tc.shortCommands > 1 {
				wantWidth = 3
			}
			if width != wantWidth || storeCommands != wantStoreCommands {
				t.Fatalf("short width=%d, store commands=%d; want width=%d, store commands=%d", width, storeCommands, wantWidth, wantStoreCommands)
			}
			critical := groups[1][0].command
			if strings.Contains(critical, "-short") || !strings.Contains(critical, "TestZCritical") || !strings.Contains(critical, fmt.Sprintf("GOMAXPROCS=%d go test -p %d", tc.allowance, tc.allowance)) {
				t.Fatalf("full critical command changed: %s", critical)
			}
		})
	}
	p := affectedTestPlan{module: ".sparkwing", packages: []string{store}}
	if groups := affectedTestGroups([]affectedTestPlan{p}, 7); len(groups) != 1 || len(groups[0]) != 1 {
		t.Fatalf("nested module gained root store shards: %q", groups)
	}
	if groups := affectedTestGroups([]affectedTestPlan{{module: "."}}, 7); len(groups) != 0 {
		t.Fatalf("empty selection runs commands: %q", groups)
	}
}
