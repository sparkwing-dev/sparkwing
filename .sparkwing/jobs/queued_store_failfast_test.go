package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestQueuedLandKeepsModuleWidthsAndThreeStoreShards(t *testing.T) {
	if goStepParallelism(affectedPushHost()) != 7 {
		t.Skip("module-allocation proof needs the normal seven-core allowance")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n")
	markers := t.TempDir()
	helper := fmt.Sprintf(`func record(t *testing.T) {
 t.Helper()
 data,err:=json.Marshal(map[string]any{"pid":os.Getpid(),"width":runtime.GOMAXPROCS(0),"short":testing.Short(),"flags":os.Getenv("GOFLAGS")});if err!=nil { t.Fatal(err) }
 if err:=os.WriteFile(filepath.Join(%q,t.Name()),data,0600);err!=nil { t.Fatal(err) }
}

`, markers)
	imports := "import (\"encoding/json\"; \"os\"; \"path/filepath\"; \"runtime\"; \"testing\")\n"
	writeGoFile(t, filepath.Join(root, "internal/store/store_test.go"), "package store\n"+imports+helper+fmt.Sprintf(`
func TestCShard(t *testing.T) { record(t) }
func TestMShard(t *testing.T) { record(t) }
func TestAShard(t *testing.T) { record(t) }
%s
func TestZStoreCritical(t *testing.T) {
 if testing.Short() { t.Skip("full critical body") }
 for _,name:=range []string{"TestCShard","TestMShard","TestAShard","TestOther","TestPipeline"} {
  if _,err:=os.Stat(filepath.Join(%q,name));err!=nil { t.Fatalf("critical preceded %%s: %%v",name,err) }
 }
 record(t)
}
`, criticalTestMarker, markers))
	writeGoFile(t, filepath.Join(root, "other/other_test.go"), "package other\n"+imports+helper+"func TestOther(t *testing.T) { record(t) }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/fixture_test.go"), "package pipelines\n"+imports+helper+"func TestPipeline(t *testing.T) { record(t) }\n")
	gitCommitAll(t, root, "queued store module fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n\n// Select root module tests.\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/go.mod"), "module fixture-pipelines\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select both queued modules")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Preserve module allocation and store shards"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
		t.Fatal(err)
	}
	storePIDs := map[int]bool{}
	for _, expected := range []struct {
		name  string
		width int
		short bool
	}{
		{"TestCShard", 1, true},
		{"TestMShard", 1, true},
		{"TestAShard", 1, true},
		{"TestOther", 3, true},
		{"TestPipeline", 1, true},
		{"TestZStoreCritical", 7, false},
	} {
		data, err := os.ReadFile(filepath.Join(markers, expected.name))
		if err != nil {
			t.Fatalf("selected body %s missing: %v", expected.name, err)
		}
		var got struct {
			PID   int
			Width int
			Short bool
			Flags string
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Width != expected.width || got.Short != expected.short || got.Flags != "-mod=readonly" {
			t.Errorf("%s settings=%+v; want width%d short%v and owned readonly flags", expected.name, got, expected.width, expected.short)
		}
		if expected.width == 1 && expected.name != "TestPipeline" {
			storePIDs[got.PID] = true
		}
	}
	if len(storePIDs) != 3 {
		t.Errorf("store short bodies used %d process(es), want three shards", len(storePIDs))
	}
}

func TestQueuedStoreShardsBelongToOnePackageAdmission(t *testing.T) {
	packages, groups := queuedTestPackages([]affectedTestPlan{{module: ".", packages: []string{storeTestPackage, "fixture/other"}}}, 7)
	var store *queuedTestPackage
	count, laneCount := 0, 0
	for _, item := range packages {
		if item.pkg == storeTestPackage {
			store = item
			count++
			if len(item.commands) != 3 {
				t.Fatalf("store package owns %d commands, want three", len(item.commands))
			}
		}
	}
	if count != 1 {
		t.Fatalf("store has %d admission items, want one", count)
	}
	for _, group := range groups {
		for _, lane := range group {
			for _, item := range lane.packages {
				if item.pkg == storeTestPackage {
					laneCount++
					if item != store || lane.capacity != 3 || lane.runtimeWidth != 1 {
						t.Fatalf("store's atomic item was split or lost its three slots: %+v", lane)
					}
				}
			}
		}
	}
	if laneCount != 1 {
		t.Fatalf("store appears in %d admission lanes, want one", laneCount)
	}
}

func TestQueuedLandStoreFailureJoinsEveryShardAndStopsPendingWork(t *testing.T) {
	if goStepParallelism(affectedPushHost()) != 7 {
		t.Skip("module-allocation proof needs the normal seven-core allowance")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n")
	initial := map[string]int{"/store-C": 1, "/store-M": 1, "/store-A": 1, "/other0": 3, "/other1": 3, "/other2": 3, "/pipeline": 1}
	type body struct {
		PID, Width int
		Short      bool
		Flags      string
	}
	var mu sync.Mutex
	markers := map[string][]body{}
	ready, release := make(chan struct{}), make(chan struct{})
	var readyOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value body
		if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		markers[r.URL.Path] = append(markers[r.URL.Path], value)
		all := true
		for path := range initial {
			all = all && len(markers[path]) == 1
		}
		if all {
			readyOnce.Do(func() { close(ready) })
		}
		mu.Unlock()
		if r.URL.Path == "/store-C" {
			select {
			case <-ready:
			case <-r.Context().Done():
			}
		} else if _, active := initial[r.URL.Path]; active {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	t.Cleanup(func() { readyOnce.Do(func() { close(ready) }); unblock(); server.Close() })
	helper := fmt.Sprintf(`func signal(t *testing.T, path string) {
 t.Helper()
 data,_:=json.Marshal(map[string]any{"PID":os.Getpid(),"Width":runtime.GOMAXPROCS(0),"Short":testing.Short(),"Flags":os.Getenv("GOFLAGS")})
 client:=&http.Client{Timeout:30*time.Second}; response,err:=client.Post(%q+path,"application/json",bytes.NewReader(data));if err!=nil { t.Fatal(err) };response.Body.Close()
}
`, server.URL)
	imports := "import (\"bytes\"; \"encoding/json\"; \"net/http\"; \"os\"; \"runtime\"; \"testing\"; \"time\")\n"
	writeGoFile(t, filepath.Join(root, "internal/store/store_test.go"), "package store\n"+imports+helper+`
func TestCShard(t *testing.T) { signal(t,"/store-C"); t.Fatal("queued store C first failure") }
func TestGAfterFailure(t *testing.T) { signal(t,"/store-C-later"); t.Fatal("queued store C later failure") }
func TestMShard(t *testing.T) { signal(t,"/store-M") }
func TestNAfterFailure(t *testing.T) { signal(t,"/store-M-later"); t.Fatal("queued store M later failure") }
func TestAShard(t *testing.T) { signal(t,"/store-A") }
func TestDAfterFailure(t *testing.T) { signal(t,"/store-A-later"); t.Fatal("queued store A later failure") }
`+criticalTestMarker+`
func TestZStoreCritical(t *testing.T) { if testing.Short(){t.Skip("full critical")};signal(t,"/store-critical") }
`)
	for i := range 3 {
		writeGoFile(t, filepath.Join(root, fmt.Sprintf("a_hold%d/fixture_test.go", i)), "package fixture\n"+imports+helper+fmt.Sprintf("func TestAOther(t *testing.T) { signal(t,%q) }\nfunc TestZOtherDone(t *testing.T) { signal(t,%q) }\n", fmt.Sprintf("/other%d", i), fmt.Sprintf("/other%d-done", i)))
	}
	writeGoFile(t, filepath.Join(root, "z_pending/fixture_test.go"), "package fixture\n"+imports+helper+"func TestPending(t *testing.T) { signal(t,\"/root-pending\") }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/fixture_test.go"), "package pipelines\n"+imports+helper+"func TestAPipeline(t *testing.T) { signal(t,\"/pipeline\") }\nfunc TestZPipelineDone(t *testing.T) { signal(t,\"/pipeline-done\") }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/z_pending/fixture_test.go"), "package fixture\n"+imports+helper+"func TestPending(t *testing.T) { signal(t,\"/pipeline-pending\") }\n")
	writeGoFile(t, filepath.Join(root, "critical_module/go.mod"), "module fixture-critical-only\n\ngo 1.26.0\n")
	writeGoFile(t, filepath.Join(root, "critical_module/fixture_test.go"), "package fixture\n"+imports+helper+"//sparkwing:critical\nfunc TestPendingModule(t *testing.T) { if testing.Short(){t.Skip(\"full critical\")};signal(t,\"/module-critical\") }\n")
	gitCommitAll(t, root, "queued shard failure fixture")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.1\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/go.mod"), "module fixture-pipelines\n\ngo 1.26.0\n")
	gitCommitAll(t, root, "select queued root and pipeline")
	host, plan, _ := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Keep admitted store shards together on failure"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, unblock)
	defer stop()
	observed := false
	err := runObservedQueuedStep(ctx, host, plan, nil, func(record nodeproto.FrameLog) {
		if strings.Contains(record.Message, "queued store C first failure") {
			observed = true
			unblock()
		}
	})
	if !observed {
		t.Error("store release did not follow a genuine emitted first failure")
	}
	for _, phrase := range []string{"queued store C first failure", "queued store C later failure", "queued store M later failure", "queued store A later failure"} {
		if err == nil || !strings.Contains(err.Error(), phrase) {
			t.Errorf("admitted shard failure %q missing: %v", phrase, err)
		}
	}
	var summary string
	for _, record := range host.Records() {
		if record.Event == "summary" {
			summary = record.Msg
		}
	}
	_, skipped, _ := strings.Cut(summary, "Skipped short packages:\n")
	short, critical, _ := strings.Cut(skipped, "Skipped critical packages:\n")
	critical, _, _ = strings.Cut(critical, "**Local rerun:")
	for _, entry := range []string{"- `.`: `github.com/sparkwing-dev/sparkwing/z_pending`", "- `.sparkwing`: `fixture-pipelines/z_pending`"} {
		if strings.Count(short, entry) != 1 {
			t.Errorf("pending selected package must appear once in short inventory: %s\n%s", entry, summary)
		}
	}
	if strings.Contains(short, "`github.com/sparkwing-dev/sparkwing/internal/store`") || strings.Count(critical, "- `critical_module`: `fixture-critical-only`") != 1 {
		t.Errorf("admitted store must not be skipped; untouched selected critical module must be skipped once:\n%s", summary)
	}
	mu.Lock()
	defer mu.Unlock()
	storePIDs := map[int]bool{}
	for path, width := range initial {
		values := markers[path]
		if len(values) != 1 || values[0].Width != width || !values[0].Short || values[0].Flags != "-mod=readonly" {
			t.Errorf("initial %s body=%+v; want once at width%d short/readonly", path, values, width)
		}
		if strings.HasPrefix(path, "/store-") && len(values) == 1 {
			storePIDs[values[0].PID] = true
		}
	}
	if len(storePIDs) != 3 {
		t.Errorf("store used %d processes, want three admitted shards", len(storePIDs))
	}
	for _, path := range []string{"/store-C-later", "/store-M-later", "/store-A-later", "/other0-done", "/other1-done", "/other2-done", "/pipeline-done"} {
		if len(markers[path]) != 1 {
			t.Errorf("already-running body %s executed %d times, want once", path, len(markers[path]))
		}
	}
	for _, path := range []string{"/root-pending", "/pipeline-pending", "/store-critical", "/module-critical"} {
		if len(markers[path]) != 0 {
			t.Errorf("pending body %s started after store failure", path)
		}
	}
}

func TestQueuedLandModuleFallbackKeepsStoreInventoryAndCoverage(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(fmt.Sprintf("first-module-fails-%v", fail), func(t *testing.T) {
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n")
			markers := t.TempDir()
			helper := fmt.Sprintf(`func record(t *testing.T) {
 t.Helper()
 data,_:=json.Marshal(map[string]int{"pid":os.Getpid(),"width":runtime.GOMAXPROCS(0)})
 if err:=os.WriteFile(filepath.Join(%q,t.Name()),data,0600);err!=nil { t.Fatal(err) }
}
`, markers)
			imports := "import (\"encoding/json\"; \"os\"; \"path/filepath\"; \"runtime\"; \"testing\")\n"
			writeGoFile(t, filepath.Join(root, ".sparkwing/fixture_test.go"), "package pipelines\n"+imports+helper+fmt.Sprintf("func TestPipeline(t *testing.T) { record(t); if %v { t.Fatal(\"fallback first module failed\") } }\n", fail))
			writeGoFile(t, filepath.Join(root, "internal/store/fixture_test.go"), "package store\n"+imports+helper+fmt.Sprintf(`
func TestCShard(t *testing.T) { record(t) }
func TestMShard(t *testing.T) { record(t) }
func TestAShard(t *testing.T) { record(t) }
%s
func TestZStoreCritical(t *testing.T) {
 if testing.Short() { t.Skip("full critical body") }
 for _,name:=range []string{"TestPipeline","TestOne","TestTwo","TestCShard","TestMShard","TestAShard"} {
  if _,err:=os.Stat(filepath.Join(%q,name));err!=nil { t.Fatalf("critical preceded %%s: %%v",name,err) }
 }
 record(t)
}
`, criticalTestMarker, markers))
			for _, name := range []string{"One", "Two"} {
				dir := strings.ToLower(name)
				writeGoFile(t, filepath.Join(root, dir, "go.mod"), "module fixture-"+dir+"\n\ngo 1.26.0\n")
				writeGoFile(t, filepath.Join(root, dir, "fixture_test.go"), "package fixture\n"+imports+helper+"func Test"+name+"(t *testing.T) { record(t) }\n")
			}
			gitCommitAll(t, root, "fallback queued modules")
			plans := []affectedTestPlan{
				{module: ".sparkwing", packages: []string{"fixture-pipelines"}},
				{module: ".", packages: []string{storeTestPackage}, critical: map[string][]string{storeTestPackage: {"TestZStoreCritical"}}},
				{module: "one", packages: []string{"fixture-one"}},
				{module: "two", packages: []string{"fixture-two"}},
			}
			summary, err := runQueuedTestPackages(newTestContext(t), plans, hostShape{cpus: 7}, t.TempDir(), &goTestTimings{})
			if fail {
				if err == nil || !strings.Contains(err.Error(), "fallback first module failed") {
					t.Fatalf("first module failure missing: %v", err)
				}
				_, skipped, ok := strings.Cut(summary, "Skipped short packages:\n")
				if !ok {
					t.Fatal("missing skipped-short inventory")
				}
				short, critical, ok := strings.Cut(skipped, "Skipped critical packages:\n")
				if !ok {
					t.Fatal("missing skipped-critical inventory")
				}
				critical, _, _ = strings.Cut(critical, "**Local rerun:")
				entry := "- `.`: `github.com/sparkwing-dev/sparkwing/internal/store`"
				if strings.Count(short, entry) != 1 || strings.Count(critical, entry) != 1 {
					t.Errorf("pending three-shard store must appear once per phase:\n%s", summary)
				}
				for _, name := range []string{"TestCShard", "TestMShard", "TestAShard", "TestZStoreCritical", "TestOne", "TestTwo"} {
					if _, err := os.Stat(filepath.Join(markers, name)); !os.IsNotExist(err) {
						t.Errorf("pending module/package body %s started: %v", name, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pids := map[int]bool{}
			for _, name := range []string{"TestPipeline", "TestCShard", "TestMShard", "TestAShard", "TestOne", "TestTwo", "TestZStoreCritical"} {
				data, err := os.ReadFile(filepath.Join(markers, name))
				if err != nil {
					t.Fatalf("fallback body %s omitted: %v", name, err)
				}
				var value struct{ PID, Width int }
				if err := json.Unmarshal(data, &value); err != nil {
					t.Fatal(err)
				}
				width := 3
				if name == "TestCShard" || name == "TestMShard" || name == "TestAShard" {
					width = 1
					pids[value.PID] = true
				}
				if value.Width != width {
					t.Errorf("fallback %s width=%d, want%d", name, value.Width, width)
				}
			}
			if len(pids) != 3 {
				t.Errorf("store-only fallback used %d shard processes, want three", len(pids))
			}
		})
	}
}
