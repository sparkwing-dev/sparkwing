package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func TestHereGuideResumeExamplesUseSealedGrammar(t *testing.T) {
	body, err := docs.ReadRaw("hotfix-runner")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "sparkwing ") || !strings.Contains(line, "--resume ") {
			continue
		}
		inv, err := clisurface.Parse("sparkwing", strings.Fields(line)[1:])
		if err != nil || inv.Value("resume") == "" || inv.Value("profile") == "" || inv.Pipeline != "" || len(inv.Args) != 0 {
			t.Fatalf("invalid resume example %q: %v", line, err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("here guide has no executable resume example")
	}
}
