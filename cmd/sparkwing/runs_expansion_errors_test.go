package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunErrorsListGeneratorWhoseExpansionFailed(t *testing.T) {
	detail := controllerwire.RunDetail{ID: "run", Status: "failed", Jobs: []controllerwire.JobDetail{
		{ID: "ok", Status: "done", Outcome: "success", Expansion: "accepted"},
		{ID: "gen", Status: "done", Outcome: "success", Expansion: "failed", Attempt: 1, Error: "expansion failed: evaluation source acquisition failed"},
	}}
	var out bytes.Buffer
	if err := writeRunErrors(&out, detail, "pretty"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "run\tgen\tattempt=1\texpansion failed: evaluation source acquisition failed\n" {
		t.Fatalf("errors view = %q", got)
	}
	rows, err := runFailures(t.Context(), nil, detail, "job")
	if err != nil || len(rows) != 1 || rows[0].JobID != "gen" || !strings.Contains(rows[0].Message, "evaluation source acquisition failed") {
		t.Fatalf("failures = %+v, %v", rows, err)
	}
}
