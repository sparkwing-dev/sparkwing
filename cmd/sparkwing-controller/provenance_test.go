package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestProvenanceLineRendersAllFields(t *testing.T) {
	p := provenance{Version: "v0.9.0", Commit: "abc1234", Schema: 3}
	got := p.line()
	want := "version v0.9.0, runs-store schema 3, commit abc1234"
	if got != want {
		t.Fatalf("line() = %q, want %q", got, want)
	}
}

func TestProvenanceLineMarksDirtyCommit(t *testing.T) {
	p := provenance{Version: "v0.9.0", Commit: "abc1234", Modified: true, Schema: 3}
	if got := p.line(); !strings.Contains(got, "abc1234+dirty") {
		t.Fatalf("line() = %q, want a +dirty commit marker", got)
	}
}

func TestProvenanceLineFallsBackWhenUnstamped(t *testing.T) {
	p := provenance{Schema: 3}
	got := p.line()
	if !strings.Contains(got, "version (unknown)") || !strings.Contains(got, "commit (unknown)") {
		t.Fatalf("line() = %q, want (unknown) fallbacks for version and commit", got)
	}
	if !strings.Contains(got, "schema 3") {
		t.Fatalf("line() = %q, want the embedded schema version present", got)
	}
}

func TestReadProvenanceReportsEmbeddedSchema(t *testing.T) {
	if got := readProvenance().Schema; got != store.ExpectedSchemaVersion() {
		t.Fatalf("readProvenance().Schema = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
