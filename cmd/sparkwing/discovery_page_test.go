package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func discoveryOutput(t *testing.T, args ...string) ([]map[string]any, map[string]any, []byte) {
	t.Helper()
	cmd := outputContractCommand(t, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, stderr.String())
	}
	rows := decodeOutputRecords(t, out)
	if len(rows) == 0 || rows[len(rows)-1]["kind"] != "page" {
		t.Fatalf("missing page summary: %s", out)
	}
	page := rows[len(rows)-1]
	rows = rows[:len(rows)-1]
	if page["returned"] != float64(len(rows)) {
		t.Fatalf("summary disagrees with records: %v", page)
	}
	return rows, page, out
}

func TestDiscoveryCommandsPaginationAndFullTreeCounts(t *testing.T) {
	all, allPage, _ := discoveryOutput(t, "commands", "--limit", "0")
	if len(all) < 80 || allPage["truncated"] != false {
		t.Fatal("exhaustive index did not include the full tree")
	}
	want := make([]string, len(all))
	counts := map[string]any{}
	for i, row := range all {
		want[i] = row["path"].(string)
		counts[want[i]] = row["subcommand_count"]
	}
	var got []string
	cursor := ""
	for {
		args := []string{"commands"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		rows, page, output := discoveryOutput(t, args...)
		if len(rows) > 40 || len(output) > 20_000 || page["total"] != float64(len(all)) {
			t.Fatalf("unbounded or inconsistent page: %v, %d bytes", page, len(output))
		}
		for _, row := range rows {
			path := row["path"].(string)
			got = append(got, path)
			if row["subcommand_count"] != counts[path] {
				t.Fatalf("page-local child count for %s", path)
			}
		}
		if page["truncated"] == false {
			break
		}
		next := page["next_cursor"].(string)
		if next <= cursor {
			t.Fatal("cursor did not advance lexically")
		}
		cursor = next
	}
	if !slices.Equal(got, want) {
		t.Fatal("pagination duplicated, omitted or reordered commands")
	}
	rows, _, _ := discoveryOutput(t, "commands", "--query", "version hold", "--limit", "1")
	if len(rows) != 1 || rows[0]["path"] != "sparkwing version hold" {
		t.Fatalf("query was applied after limit: %v", rows)
	}
	rows, _, _ = discoveryOutput(t, "commands", "--path", "runs", "--query", "runs", "--limit", "1")
	if rows[0]["path"] != "sparkwing runs" || rows[0]["subcommand_count"] != counts["sparkwing runs"] {
		t.Fatal("query or page shrank the group's child count")
	}
}

func TestDiscoveryDocsBudgetAndSelectedBody(t *testing.T) {
	rows, page, out := discoveryOutput(t, "docs", "search", "--query", "cache")
	if len(rows) != 20 || page["truncated"] != true || len(out) > 15_000 {
		t.Fatalf("search exceeded bounded snippet contract: %v, %d bytes", page, len(out))
	}
	for _, row := range rows {
		if _, exists := row["body"]; exists {
			t.Fatal("search included a body without --body")
		}
		if utf8.RuneCountInString(row["snippet"].(string)) > 113 {
			t.Fatal("search snippet exceeded its budget")
		}
	}
	first := rows[0]
	args := []string{"docs", "read", "--topic", first["slug"].(string), "--section", fmt.Sprint(first["start_line"])}
	cmd := outputContractCommand(t, args...)
	body, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	records := decodeOutputRecords(t, body)
	if len(records) != 1 || records[0]["kind"] != "document" || !strings.Contains(records[0]["text"].(string), first["heading"].(string)) {
		t.Fatalf("selected section read did not return the chosen body: %s", body)
	}
	plain, err := outputContractCommand(t, append(args, "--output", "plain")...).Output()
	if err != nil || string(plain) != records[0]["text"] {
		t.Fatalf("plain selected body differs: %v", err)
	}
	explicit, _, _ := discoveryOutput(t, "docs", "search", "--query", "cache", "--limit", "1", "--body")
	if explicit[0]["body"] == nil || explicit[0]["body"] == "" {
		t.Fatal("explicit body access disappeared")
	}
}

func TestDiscoveryDocsRankedPaginationAndListFiltering(t *testing.T) {
	all, _, _ := discoveryOutput(t, "docs", "search", "--query", "Memoize key", "--limit", "0")
	var got []map[string]any
	cursor := ""
	for {
		args := []string{"docs", "search", "--query", "Memoize key", "--limit", "7"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		rows, page, _ := discoveryOutput(t, args...)
		got = append(got, rows...)
		if page["truncated"] == false {
			break
		}
		next := page["next_cursor"].(string)
		if next == cursor {
			t.Fatal("ranked cursor did not advance")
		}
		cursor = next
	}
	wantJSON, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Fatal("pagination changed ranked search order or omitted results")
	}
	rows, page, _ := discoveryOutput(t, "docs", "list")
	if len(rows) != 40 || page["truncated"] != true {
		t.Fatal("docs index is unbounded")
	}
	rows, _, _ = discoveryOutput(t, "docs", "list", "--query", "migrations/v0.46.0", "--limit", "1")
	if len(rows) != 1 || rows[0]["slug"] != "migrations/v0.46.0" {
		t.Fatalf("late topic was lost before filtering: %v", rows)
	}
	rows, page, _ = discoveryOutput(t, "commands", "--query", "not-a-real-command-query")
	if len(rows) != 0 || page["total"] != float64(0) || page["truncated"] != false {
		t.Fatal("empty search does not report an empty page")
	}
}

func TestDiscoveryRejectsInvalidSelectionAndKeepsExportsExhaustive(t *testing.T) {
	for _, args := range [][]string{
		{"commands", "--limit", "-1"},
		{"commands", "--cursor", "missing"},
		{"commands", "--format", "markdown", "--limit", "2"},
		{"docs", "search", "-q", "cache", "--cursor", "missing"},
		{"docs", "list", "--limit", "-1"},
		{"docs", "read", "--topic", "caching", "--section", "0"},
		{"docs", "read", "--topic", "caching", "--section", "999999"},
		{"docs", "read", "--topic", "caching", "--section", "1", "--web"},
	} {
		out, err := outputContractCommand(t, args...).Output()
		if err == nil || len(out) != 0 {
			t.Fatalf("%v: expected failure without partial stdout, got %v %s", args, err, out)
		}
	}
	out, err := outputContractCommand(t, "commands", "--format", "markdown", "--output", "plain").Output()
	if err != nil || !strings.Contains(string(out), "sparkwing version hold") {
		t.Fatalf("explicit export was paginated: %v", err)
	}
	if got := truncateLine(strings.Repeat("é", 150)); !utf8.ValidString(got) || utf8.RuneCountInString(got) != 113 {
		t.Fatal("snippet truncation corrupted Unicode")
	}
}
