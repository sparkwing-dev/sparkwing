package store

import "testing"

func TestTranslateSchemaKeepsQuotedBlobNames(t *testing.T) {
	const neutral = "-- BLOB stays a comment\nCREATE TABLE x (value BLOB, name TEXT CHECK(name <> 'BLOB'), \"BLOB\" TEXT);"
	got, err := TranslateSchema(neutral, DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	want := "-- BLOB stays a comment\nCREATE TABLE x (value BYTEA, name TEXT CHECK(name <> 'BLOB'), \"BLOB\" TEXT);"
	if got != want {
		t.Fatalf("translation = %q", got)
	}
	if _, err = TranslateSchema(neutral, Dialect(100)); err == nil {
		t.Fatal("unknown dialect accepted")
	}
	if _, err = TranslateSchema("CREATE TABLE x (v BLOB); PRAGMA foreign_keys=OFF;", DialectPostgres); err == nil {
		t.Fatal("non-neutral pragma accepted")
	}
}
