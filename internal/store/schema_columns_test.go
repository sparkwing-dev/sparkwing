package store

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

var (
	createTableRe  = regexp.MustCompile(`(?is)CREATE TABLE\s+([a-z_][a-z0-9_]*)\s*\((.*?)\n\);`)
	schemaColumnRe = regexp.MustCompile(`(?m)^\s+([a-z_][a-z0-9_]*)\s+(?:TEXT|INTEGER|BIGINT|BLOB|REAL|BOOLEAN)\b`)
)

// safety: read from the embedded schema, so a guard built on it judges a new table the day it is added.
func schemaTablesWithColumn(t *testing.T, column string) []string {
	t.Helper()
	if n := len(createTableRe.FindAllStringSubmatch(schemaNeutral, -1)); n < 90 {
		t.Fatalf("parsed only %d tables from schema.sql; the column reader no longer matches it", n)
	}
	return tablesWithColumn(column)
}

var teamColumnTables = sync.OnceValue(func() []string { return tablesWithColumn("team") })

func tablesWithColumn(column string) []string {
	var out []string
	for _, m := range createTableRe.FindAllStringSubmatch(schemaNeutral, -1) {
		for _, c := range schemaColumnRe.FindAllStringSubmatch(m[2], -1) {
			if c[1] == column {
				out = append(out, strings.ToLower(m[1]))
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func statementTables(sql string, candidates []string) []string {
	var out []string
	tables, _ := sqlRelations(scopeTokens(sql))
	for _, name := range tables {
		if slices.Contains(candidates, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
