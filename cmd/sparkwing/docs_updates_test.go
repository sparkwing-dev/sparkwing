package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestUpdateGuideExamplesUseFinalGrammar(t *testing.T) {
	body, err := docs.ReadRaw("updates")
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, unit := range codeUnits(body) {
		if !strings.HasPrefix(unit, "sparkwing ") {
			continue
		}
		if _, err := clisurface.Parse("sparkwing", strings.Fields(unit)[1:]); err != nil {
			t.Errorf("update example %q: %v", unit, err)
		}
		commands++
	}
	if commands == 0 {
		t.Fatal("update guide contains no CLI examples")
	}
}
