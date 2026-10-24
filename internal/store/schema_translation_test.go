package store

import (
	"slices"
	"testing"
)

func TestSplitStatementsKeepsCommentsAndQuotedSemicolons(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{
			name:   "schema line comment",
			script: "-- every timestamp is Unix nanoseconds; persisted JSON envelopes carry a version.\nCREATE TABLE x (v BLOB); CREATE TABLE y (v TEXT);",
			want:   []string{"-- every timestamp is Unix nanoseconds; persisted JSON envelopes carry a version.\nCREATE TABLE x (v BLOB)", "CREATE TABLE y (v TEXT)"},
		},
		{
			name:   "block comment",
			script: "/* a; -- ' \" comment */ SELECT 1; SELECT /* ; */ 2;",
			want:   []string{"/* a; -- ' \" comment */ SELECT 1", "SELECT /* ; */ 2"},
		},
		{
			name:   "quoted values and doubled quotes",
			script: `SELECT 'it''s; -- /* fine', "a"";b"; SELECT ';';`,
			want:   []string{`SELECT 'it''s; -- /* fine', "a"";b"`, `SELECT ';'`},
		},
		{
			name:   "empty statements and final statement",
			script: " ; ; SELECT 1;; SELECT 2 ",
			want:   []string{"SELECT 1", "SELECT 2"},
		},
		{
			name:   "line comment at end of input",
			script: "SELECT 1 -- ; ' /*",
			want:   []string{"SELECT 1 -- ; ' /*"},
		},
		{
			name:   "trailing comments",
			script: "SELECT 1; -- trailing; comment\n/* ; */",
			want:   []string{"SELECT 1"},
		},
		{
			name:   "only comments and separators",
			script: "; -- comment;\n ; /* ; */;",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitStatements(tt.script)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("statements = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSchemaScannerRejectsMalformedInput(t *testing.T) {
	for _, script := range []string{
		"SELECT 1; /* unterminated;",
		"SELECT 1; SELECT 'unterminated;",
		`SELECT 1; SELECT "unterminated;`,
		"SELECT 1; SELECT 'doubled quote'';",
		`SELECT 1; SELECT "doubled quote"";`,
	} {
		t.Run(script, func(t *testing.T) {
			if got, err := splitStatements(script); err == nil || got != nil {
				t.Fatalf("split malformed input = %#v, %v", got, err)
			}
			for _, dialect := range []Dialect{DialectSQLite, DialectPostgres} {
				if got, err := translateSchema(script, dialect); err == nil || got != "" {
					t.Fatalf("translate malformed input (%s) = %q, %v", dialect, got, err)
				}
			}
		})
	}
}

func TestTranslateSchemaKeepsQuotedBlobNames(t *testing.T) {
	const neutral = "-- BLOB; stays a comment\n/* BLOB; BYTEA */ CREATE TABLE x (value BLOB, name TEXT CHECK(name <> 'it''s BLOB; BYTEA'), \"BLOB\"\"; BYTEA\" TEXT);"
	got, err := translateSchema(neutral, DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	want := "-- BLOB; stays a comment\n/* BLOB; BYTEA */ CREATE TABLE x (value BYTEA, name TEXT CHECK(name <> 'it''s BLOB; BYTEA'), \"BLOB\"\"; BYTEA\" TEXT);"
	if got != want {
		t.Fatalf("translation = %q", got)
	}
	if got, err := translateSchema(neutral, DialectSQLite); err != nil || got != neutral[:len(neutral)-1]+" STRICT;" {
		t.Fatalf("SQLite translation = %q, %v", got, err)
	}
	if _, err = translateSchema(neutral, Dialect(100)); err == nil {
		t.Fatal("unknown dialect accepted")
	}
	if _, err = translateSchema("CREATE TABLE x (v BLOB); PRAGMA foreign_keys=OFF;", DialectPostgres); err == nil {
		t.Fatal("non-neutral pragma accepted")
	}
}
