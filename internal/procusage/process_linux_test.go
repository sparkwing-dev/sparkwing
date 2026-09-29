//go:build linux

package procusage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseStatSeparatesSelfAndReapedCPU(t *testing.T) {
	fields := []string{"S", "10", "0", "0", "0", "0", "0", "0", "0", "0", "0", "125", "25", "400", "100", "0", "0", "1", "0", "12345", "0", "16"}
	raw := "20 (name with ) spaces) " + strings.Join(fields, " ")
	p := parseStat(raw, 250)
	if p.PID != 20 || p.Parent != 10 || p.Birth != 12345 || p.CPU != 600*time.Millisecond || p.ReapedCPU != 2*time.Second || !p.CPUAvailable || !p.ReapedAvailable || p.RSSAvailable || !p.AncestryAvailable {
		t.Fatalf("parsed stat incorrectly: %+v", p)
	}
	fields[11] = "unreadable"
	p = parseStat("20 (child) "+strings.Join(fields, " "), 250)
	if p.CPUAvailable || !p.AncestryAvailable {
		t.Fatalf("CPU failure erased independent measurements: %+v", p)
	}
}

func TestResidentMemoryParsing(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		multiple  bool
		bytes     int64
		available bool
	}{
		{"rollup", "Rss: 12 kB\nPss: 8 kB\n", false, 12288, true},
		{"maps", "Rss: 12 kB\nPss: 8 kB\nRss: 4 kB\n", true, 16384, true},
		{"zero", "Rss: 0 kB\n", false, 0, true},
		{"missing", "Pss: 12 kB\n", false, 0, false},
		{"duplicate rollup", "Rss: 12 kB\nRss: 4 kB\n", false, 0, false},
		{"unit", "Rss: 12 MB\n", false, 0, false},
		{"negative", "Rss: -1 kB\n", false, 0, false},
		{"overflow", "Rss: 9223372036854775807 kB\n", false, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, available := parseResidentMemory(test.raw, test.multiple)
			if got != test.bytes || available != test.available {
				t.Fatalf("got %d/%t; want %d/%t", got, available, test.bytes, test.available)
			}
		})
	}
}

func TestResidentMemoryUsesPageMapsAndReportsFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "statm"), []byte("1000 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := residentMemory(dir); ok {
		t.Fatal("missing page maps became a measured value")
	}
	if err := os.WriteFile(filepath.Join(dir, "smaps"), []byte("Rss: 12 kB\nRss: 4 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := residentMemory(dir); !ok || got != 16384 {
		t.Fatalf("full maps = %d/%t", got, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "smaps_rollup"), []byte("Rss: 24 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := residentMemory(dir); !ok || got != 24576 {
		t.Fatalf("rollup = %d/%t", got, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "smaps_rollup"), []byte("Rss: invalid kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := residentMemory(dir); ok {
		t.Fatal("invalid rollup silently used another source")
	}
}

func TestLinuxReaderBoundsMemoryReads(t *testing.T) {
	directory := t.TempDir()
	for _, process := range []struct{ pid, parent, birth int }{
		{10, 1, 100}, {20, 10, 110}, {30, 20, 120}, {40, 1, 130}, {50, 1, 150},
	} {
		path := filepath.Join(directory, strconv.Itoa(process.pid))
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		fields := []string{"S", strconv.Itoa(process.parent), "0", "0", "0", "0", "0", "0", "0", "0", "0", "125", "25", "0", "0", "0", "0", "1", "0", strconv.Itoa(process.birth), "0", "16"}
		if err := os.WriteFile(filepath.Join(path, "stat"), []byte(fmt.Sprintf("%d (fixture) %s", process.pid, strings.Join(fields, " "))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name        string
		retained    bool
		mutation    string
		wantReads   []int
		wantBytes   int64
		wantQuality string
	}{
		{"initial", false, "", []int{10, 20, 30}, 60, "sampled"},
		{"retained", true, "", []int{10, 20, 30, 50}, 110, "sampled"},
		{"identity_changes", false, "identity", []int{10, 20, 30}, 10, "partial"},
		{"parent_changes", false, "parent", []int{10, 20, 30}, 10, "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := Tree{PID: 10}
			if test.retained {
				tree.root = Identity{PID: 10, Birth: 100}
				tree.owned = map[Identity]bool{{PID: 10, Birth: 100}: true, {PID: 50, Birth: 150}: true}
			}
			beforeRoot := tree.root
			var reads []int
			snapshot := tree.readLinux(directory, func(path string) (int64, bool) {
				pid, err := strconv.Atoi(filepath.Base(path))
				if err != nil {
					t.Fatal(err)
				}
				reads = append(reads, pid)
				if pid == 20 && test.mutation != "" {
					stat := filepath.Join(path, "stat")
					raw, err := os.ReadFile(stat)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.WriteFile(stat, raw, 0o600); err != nil {
							t.Error(err)
						}
					})
					changed := strings.Replace(string(raw), " 110 ", " 999 ", 1)
					if test.mutation == "parent" {
						changed = strings.Replace(string(raw), "S 10 ", "S 1 ", 1)
					}
					if err := os.WriteFile(stat, []byte(changed), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return int64(pid), true
			})
			slices.Sort(reads)
			if !slices.Equal(reads, test.wantReads) {
				t.Fatalf("RSS read PIDs=%v, want %v", reads, test.wantReads)
			}
			if tree.root != beforeRoot || tree.previous != nil || len(tree.owned) != map[bool]int{false: 0, true: 2}[test.retained] {
				t.Fatalf("Read changed ownership or baseline: %+v", tree)
			}
			if test.mutation != "" {
				p := snapshot.Processes[20]
				if p.AncestryAvailable || p.CPUAvailable || p.ReapedAvailable || p.RSSAvailable {
					t.Fatalf("changed process retained valid measurements: %+v", p)
				}
			}
			reading := tree.Observe(snapshot)
			if reading.MemoryQuality != test.wantQuality || reading.RSSBytes != test.wantBytes {
				t.Fatalf("memory=%+v, want %d/%s", reading, test.wantBytes, test.wantQuality)
			}
		})
	}
}
