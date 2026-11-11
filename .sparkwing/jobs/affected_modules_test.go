package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAffectedModulesOverlapWithinTheirAllowanceAndJoinBeforeCritical(t *testing.T) {
	root := gateFixtureRepo(t)
	markers := t.TempDir()
	var mu sync.Mutex
	active := map[string]int{}
	release := make(chan struct{})
	var once sync.Once
	peakModules, peakWidth := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Module string
			Width  int
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		active[body.Module] = body.Width
		width := 0
		for _, n := range active {
			width += n
		}
		peakModules = max(peakModules, len(active))
		peakWidth = max(peakWidth, width)
		if len(active) == 3 {
			once.Do(func() { close(release) })
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(active, body.Module)
			mu.Unlock()
		}()
		// safety: the guard bounds a serial implementation's deadlock; overlap
		// is proved by peers reaching the release channel, not elapsed time.
		guard, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-guard.Done():
			http.Error(w, "short modules did not overlap", http.StatusConflict)
		}
	}))
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		server.Close()
	})
	helper := fmt.Sprintf(`func shortBody(t *testing.T, module string) {
 t.Helper()
 if !testing.Short() { t.Fatal("short body ran without short mode") }
 body, err := json.Marshal(map[string]any{"Module": module, "Width": runtime.GOMAXPROCS(0)})
 if err != nil { t.Fatal(err) }
 response, err := http.Post(%q, "application/json", bytes.NewReader(body))
 if err != nil { t.Fatal(err) }
 defer response.Body.Close()
 if response.StatusCode != http.StatusOK { raw, _ := io.ReadAll(response.Body); t.Fatalf("barrier: %%s", raw) }
 data, err := json.Marshal(map[string]any{"width": runtime.GOMAXPROCS(0), "flags": os.Getenv("GOFLAGS")})
 if err != nil { t.Fatal(err) }
 f, err := os.OpenFile(filepath.Join(%q, module), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
 if err != nil { t.Fatal(err) }
 defer f.Close()
 if _, err := f.Write(append(data, '\n')); err != nil { t.Fatal(err) }
}
`, server.URL, markers)
	imports := "import (\"bytes\"; \"encoding/json\"; \"io\"; \"net/http\"; \"os\"; \"path/filepath\"; \"runtime\"; \"testing\")\n"
	writeGoFile(t, filepath.Join(root, "leaf", "leaf.go"), "package leaf\nconst Value = 1\n")
	writeGoFile(t, filepath.Join(root, "leaf", "leaf_test.go"), "package leaf\n"+imports+helper+fmt.Sprintf(`
func TestShort(t *testing.T) { shortBody(t, "root"); t.Fatal("root short failure retained") }
%s
func TestCritical(t *testing.T) {
 if testing.Short() { t.Skip("full critical only") }
 for _, name := range []string{"root", "pipeline", "sdk"} {
  if _, err := os.ReadFile(filepath.Join(%q, name)); err != nil { t.Fatalf("critical started before %%s finished: %%v", name, err) }
 }
 if runtime.GOMAXPROCS(0) != 7 { t.Fatalf("critical width=%%d", runtime.GOMAXPROCS(0)) }
 if err := os.WriteFile(filepath.Join(%q, "critical"), []byte("full"), 0600); err != nil { t.Fatal(err) }
}
`, criticalTestMarker, markers, markers))
	for _, module := range []struct{ dir, name, body, pkg string }{
		{".sparkwing", "fixture-pipelines", "pipeline", "pipelines"},
		{"sparkwing", "fixture-sdk", "sdk", "sdk"},
	} {
		writeGoFile(t, filepath.Join(root, module.dir, "go.mod"), "module "+module.name+"\n\ngo 1.26\nrequire fixture v0.0.0\nreplace fixture => ..\n")
		writeGoFile(t, filepath.Join(root, module.dir, "overlap_test.go"), "package "+module.pkg+"_test\n"+imports+"import _ \"fixture/leaf\"\n"+helper+fmt.Sprintf("func TestShort(t *testing.T) { shortBody(t, %q) }\n", module.body))
	}
	gitCommitAll(t, root, "module fixture")
	err := runAffectedTestFiles(newTestContext(t), []string{"leaf/leaf.go"}, "module overlap fixture", hostShape{cpus: 16})
	if err == nil || !strings.Contains(err.Error(), "root short failure retained") || strings.Contains(err.Error(), "did not overlap") || strings.Contains(err.Error(), "critical started before") {
		t.Fatalf("short failure must preserve overlap, sibling completion and full critical tests: %v", err)
	}
	for name, width := range map[string]int{"root": 5, "pipeline": 1, "sdk": 1} {
		data, err := os.ReadFile(filepath.Join(markers, name))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s body ran %d times, want once", name, len(lines))
		}
		var body struct {
			Width int
			Flags string
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if body.Width != width || body.Flags != affectedTestGoFlags {
			t.Fatalf("%s settings=%+v, want width %d and owned flags", name, body, width)
		}
	}
	if _, err := os.ReadFile(filepath.Join(markers, "critical")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peakModules != 3 || peakWidth != 7 {
		t.Fatalf("overlap=%d modules, width=%d; want three modules sharing seven", peakModules, peakWidth)
	}
}

func TestAffectedModuleGroupsStayWithinTheAllowanceAndFinishShortsFirst(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowance int
		modules   []string
		short     [][]int
	}{
		{"one-worker fallback", 1, []string{".sparkwing", "."}, [][]int{{1}, {1}}},
		{"two modules at two", 2, []string{".sparkwing", "."}, [][]int{{1, 1}}},
		{"three modules at two", 2, []string{".sparkwing", ".", "sparkwing"}, [][]int{{2}, {2}, {2}}},
		{"three modules at three", 3, []string{".sparkwing", ".", "sparkwing"}, [][]int{{1, 1, 1}}},
		{"store fallback at reduced root allowance", 4, []string{".sparkwing", "."}, [][]int{{1, 3}}},
		{"pipeline and store", 7, []string{".sparkwing", "."}, [][]int{{1, 1, 1, 1, 3}}},
		{"all three modules", 7, []string{".sparkwing", ".", "sparkwing"}, [][]int{{1, 1, 1, 1, 1, 2}}},
		{"root last", 7, []string{".sparkwing", "sparkwing", "."}, [][]int{{1, 1, 1, 1, 1, 2}}},
		{"nested modules without root", 7, []string{".sparkwing", "sparkwing"}, [][]int{{1, 6}}},
		{"SDK alone", 7, []string{"sparkwing"}, [][]int{{7}}},
		{"root alone", 7, []string{"."}, [][]int{{1, 1, 1, 4}}},
		{"empty", 7, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var plans []affectedTestPlan
			for _, module := range tc.modules {
				packages := []string{"fixture/nested"}
				if module == "." {
					packages = []string{"github.com/sparkwing-dev/sparkwing/internal/store", "fixture/other"}
				}
				plans = append(plans, affectedTestPlan{module: module, packages: packages, critical: map[string][]string{packages[0]: {"TestGuard"}}})
			}
			var widths [][]int
			critical := map[string]int{}
			for _, group := range affectedTestGroups(plans, tc.allowance) {
				var short []int
				sum := 0
				for _, command := range group {
					var threads, packages int
					if _, err := fmt.Sscanf(command.command, "GOMAXPROCS=%d go test -p %d", &threads, &packages); err != nil {
						t.Fatal(err)
					}
					if threads != packages {
						t.Fatalf("runtime and package width differ: %s", command.command)
					}
					sum += packages
					if strings.Contains(command.command, "-short ") {
						if len(critical) != 0 {
							t.Fatal("short commands run after critical tests")
						}
						short = append(short, packages)
					} else {
						if len(group) != 1 || packages != tc.allowance || !strings.Contains(command.command, "TestGuard") {
							t.Fatalf("critical tests lost their full allowance or selection: %+v", group)
						}
						critical[command.module]++
					}
				}
				if sum > tc.allowance {
					t.Fatalf("group consumes %d, allowance %d: %+v", sum, tc.allowance, group)
				}
				if len(short) > 0 {
					slices.Sort(short)
					widths = append(widths, short)
				}
			}
			if len(widths) != len(tc.short) {
				t.Fatalf("short groups=%v, want %v", widths, tc.short)
			}
			for i := range widths {
				if !slices.Equal(widths[i], tc.short[i]) {
					t.Fatalf("short groups=%v, want %v", widths, tc.short)
				}
			}
			for _, module := range tc.modules {
				if critical[module] != 1 {
					t.Fatalf("%s critical commands=%d, want one", module, critical[module])
				}
			}
		})
	}
	plans := []affectedTestPlan{{module: ".", critical: map[string][]string{"fixture/root": {"TestGuard"}}}, {module: "sparkwing"}}
	if groups := affectedTestGroups(plans, 1); len(groups) != 1 || len(groups[0]) != 1 || strings.Contains(groups[0][0].command, "-short") {
		t.Fatalf("critical-only and empty modules planned unexpected work: %+v", groups)
	}
}
