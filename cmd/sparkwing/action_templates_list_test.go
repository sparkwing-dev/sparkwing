package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExamplesJSONListsSummariesNotManifests(t *testing.T) {
	out := captureStdout(t, func() {
		if err := listTemplates("", "", "json"); err != nil {
			t.Fatal(err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("examples -o json listed nothing")
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		if len(record) != 3 || record["name"] == "" || record["category"] == "" {
			t.Fatalf("list record %s, want only name, category and summary", line)
		}
	}
	if len(out) > 16<<10 {
		t.Fatalf("examples -o json printed %d bytes; the list carries summaries, --example NAME carries the manifest", len(out))
	}
}
