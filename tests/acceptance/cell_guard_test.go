package acceptance_test

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

const skippedCellsAllowList = "../../.guards/acceptance-skips.allow"

var cellSkips = struct {
	sync.Mutex
	allowed map[string]bool
	skipped map[string]bool
	err     error
}{skipped: map[string]bool{}}

// safety: the allow-list is judged on linux only, because a target native to
// the host executes there while its cross-OS siblings skip, so the same cell
// skips on one platform and runs on another.
func cellGuardActive() bool { return runtime.GOOS == "linux" }

func loadSkippedCells() (map[string]bool, error) {
	raw, err := os.ReadFile(skippedCellsAllowList)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out, nil
}

func skipCell(t *testing.T, scenarioID, targetID, format string, args ...any) {
	t.Helper()
	cell := scenarioID + "/" + targetID
	reason := fmt.Sprintf(format, args...)
	if !cellGuardActive() {
		t.Skip(reason)
	}
	cellSkips.Lock()
	if cellSkips.allowed == nil && cellSkips.err == nil {
		cellSkips.allowed, cellSkips.err = loadSkippedCells()
	}
	allowed, err := cellSkips.allowed[cell], cellSkips.err
	if allowed {
		cellSkips.skipped[cell] = true
	}
	cellSkips.Unlock()
	if err != nil {
		t.Fatalf("read %s: %v", skippedCellsAllowList, err)
	}
	if !allowed {
		t.Fatalf("required acceptance cell %s skipped (%s); a required cell must execute, and .guards/acceptance-skips.allow only shrinks", cell, reason)
	}
	t.Skip(reason)
}

func staleSkippedCells() error {
	if !cellGuardActive() {
		return nil
	}
	for _, name := range []string{"test.run", "test.skip"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return nil
		}
	}
	allowed, err := loadSkippedCells()
	if err != nil {
		return err
	}
	var stale []string
	for cell := range allowed {
		if !cellSkips.skipped[cell] {
			stale = append(stale, cell)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("%d cell(s) in .guards/acceptance-skips.allow no longer skip; delete these lines:\n%s", len(stale), strings.Join(stale, "\n"))
}

func TestSkippedCellAllowListNamesRequiredCells(t *testing.T) {
	allowed, err := loadSkippedCells()
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, s := range acceptance.Selections() {
		required[s.ScenarioID+"/"+s.TargetID] = true
	}
	for cell := range allowed {
		if !required[cell] {
			t.Errorf(".guards/acceptance-skips.allow names %s, which is not a required cell; delete the line", cell)
		}
	}
}
