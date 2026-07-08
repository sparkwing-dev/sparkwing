package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestVersionReportCarriesEmbeddedSchema(t *testing.T) {
	r := gatherVersionReport(true)
	if r.SchemaVersion != store.ExpectedSchemaVersion() {
		t.Fatalf("version report schema_version = %d, want store.ExpectedSchemaVersion() = %d",
			r.SchemaVersion, store.ExpectedSchemaVersion())
	}
	if r.SchemaVersion == 0 {
		t.Fatal("schema_version is 0; expected a positive embedded schema version")
	}
}
