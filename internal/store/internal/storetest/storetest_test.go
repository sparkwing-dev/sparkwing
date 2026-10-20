package storetest

import (
	"strings"
	"testing"
)

func TestSchemaNameStaysInsidePostgresIdentifierLimit(t *testing.T) {
	long := strings.Repeat("TestSomethingVeryLongIndeed/subtest+case", 4)

	name := schemaName(long)

	if len(name) > 63 {
		t.Fatalf("schema name is %d bytes: %q", len(name), name)
	}
	if !strings.HasPrefix(name, "sw_test_") {
		t.Errorf("schema name = %q, want the sw_test_ prefix", name)
	}
	other := schemaName(long)
	if other == name {
		t.Fatalf("two schema names for one test collided: %q", name)
	}
	if tail := name[strings.LastIndex(name, "_")+1:]; tail == "" {
		t.Errorf("schema name %q lost its uniquifier", name)
	}
}

func TestSanitizeKeepsIdentifierCharactersOnly(t *testing.T) {
	if got, want := sanitize("Test/Name+With.Odd Bits"), "test_name_with_odd_bits"; got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
}
