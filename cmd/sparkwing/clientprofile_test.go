package main

import "testing"

func TestResolveProfileRejectsEmptySelection(t *testing.T) {
	writeProfilesFixture(t, `
profiles:
  prod: { controller: { url: https://api.example.dev } }
`)
	profile, err := resolveProfile("")
	if err == nil {
		t.Fatal("resolveProfile returned no error for an empty selection")
	}
	if profile != nil {
		t.Fatalf("resolveProfile returned profile %#v for an empty selection", profile)
	}
}
