// Package startuptest checks startup documentation against captured process records.
package startuptest

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// CheckDocumented compares one startup owner's captured stages with the documented table.
func CheckDocumented(t testing.TB, path, prefix string, emitted []string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(body), "| Stage | Timed boundary |\n")
	if !ok {
		t.Fatal("startup stage table is missing")
	}
	table, _, _ = strings.Cut(table, "\n\n")
	var documented []string
	for _, line := range strings.Split(table, "\n") {
		columns := strings.Split(line, "|")
		if len(columns) < 3 {
			t.Fatalf("malformed startup table row: %q", line)
		}
		cell := strings.TrimSpace(columns[1])
		if cell == "---" {
			continue
		}
		if !strings.HasPrefix(cell, "`") || !strings.HasSuffix(cell, "`") {
			t.Fatalf("malformed startup stage: %q", cell)
		}
		stage := strings.Trim(cell, "`")
		if !strings.HasPrefix(stage, "bootstrap_") && !strings.HasPrefix(stage, "launcher_") {
			t.Fatalf("documented stage %q has no bootstrap or launcher behavior owner", stage)
		}
		if strings.HasPrefix(stage, prefix) {
			documented = append(documented, stage)
		}
	}
	var captured []string
	for _, stage := range emitted {
		if strings.HasPrefix(stage, prefix) {
			captured = append(captured, stage)
		}
	}
	slices.Sort(documented)
	slices.Sort(captured)
	captured = slices.Compact(captured)
	if len(documented) == 0 || !slices.Equal(documented, captured) {
		t.Fatalf("startup documentation differs from emitted records: documented=%v emitted=%v", documented, captured)
	}
}
