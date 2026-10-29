package main

import (
	"os"
	"regexp"
	"testing"
)

func TestReadmeSampleRegistersTheScaffoldEntrypoint(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	quickStart := regexp.MustCompile(`sparkwing pipeline new --pipeline ([a-z0-9-]+)`).FindSubmatch(readme)
	registered := regexp.MustCompile(`sw\.Register(?:Entrypoint)?\[[^\]]*\]\("([^"]+)"`).FindSubmatch(readme)
	if quickStart == nil || registered == nil {
		t.Fatal("README lost its quick start or its registration sample")
	}
	if want := kebabToPascal(string(quickStart[1])); string(registered[1]) != want {
		t.Fatalf("README registers %q, but the scaffold's sparkwing.yaml maps %s to entrypoint %q, so a run fails with unknown_pipeline", registered[1], quickStart[1], want)
	}
}
