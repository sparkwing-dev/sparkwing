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

func TestSkewRefusalNamesBothVersionsAndRemedy(t *testing.T) {
	msg := skewRefusalMessage(&store.SkewError{DBVersion: 4, BinaryVersion: 3})
	for _, want := range []string{"schema version 4", "schema 3", "Roll the controller forward", "restore"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("skewRefusalMessage missing %q: %s", want, msg)
		}
	}
}

func TestMapStoreOpenErrorRefusesSchemaSkew(t *testing.T) {
	err := mapStoreOpenError(&store.SkewError{DBVersion: 4, BinaryVersion: 3})
	if err == nil {
		t.Fatal("mapStoreOpenError(SkewError) = nil, want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refusing to start") || !strings.Contains(msg, "schema version 4") {
		t.Fatalf("mapStoreOpenError(SkewError) = %q, want a legible skew refusal", msg)
	}
}

func TestMapStoreOpenErrorKeepsGenericFraming(t *testing.T) {
	err := mapStoreOpenError(errStub("disk is gone"))
	if err == nil {
		t.Fatal("mapStoreOpenError(stub) = nil, want an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "open state db: disk is gone") {
		t.Fatalf("mapStoreOpenError(stub) = %q, want generic open-state-db framing", msg)
	}
}

func TestMapStoreOpenErrorNilStaysNil(t *testing.T) {
	if err := mapStoreOpenError(nil); err != nil {
		t.Fatalf("mapStoreOpenError(nil) = %v, want nil", err)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
