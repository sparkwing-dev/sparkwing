package store

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
)

//go:generate go test -run ^TestSchemaErrorDocumentation$ -update-schema-docs
var updateSchemaDocs = flag.Bool("update-schema-docs", false, "regenerate schema error examples in backup-restore.md")

func TestSchemaErrorDocumentation(t *testing.T) {
	const path = "../../docs/backup-restore.md"
	const begin = "<!-- BEGIN GENERATED SCHEMA ERRORS -->"
	const end = "<!-- END GENERATED SCHEMA ERRORS -->"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start, finish := strings.Index(text, begin), strings.Index(text, end)
	if start < 0 || finish < start {
		t.Fatal("missing generated schema error markers")
	}
	want := fmt.Sprintf("%s\n```text\n%s\n\n%s\n```\n", begin,
		schemaMismatchError(expectedSchemaVersion-1, "delete <path> (or drop the database)"),
		schemaMismatchError(expectedSchemaVersion+1, "delete <path> (or drop the database)"))
	if *updateSchemaDocs {
		if err := os.WriteFile(path, []byte(text[:start]+want+text[finish:]), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if text[start:finish] != want {
		t.Fatal("schema error examples drifted; run go generate ./internal/store")
	}
}
