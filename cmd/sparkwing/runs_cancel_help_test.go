package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunsCancelHelpDeclaresLocalHome(t *testing.T) {
	found := false
	for _, spec := range cmdJobsCancel.Flags {
		if spec.Name != "home" {
			continue
		}
		found = true
		if !strings.Contains(strings.ToLower(spec.Desc), "local") {
			t.Errorf("--home description = %q, want local-mode purpose", spec.Desc)
		}
	}
	if !found {
		t.Fatal("runs cancel does not declare --home")
	}

	var out bytes.Buffer
	PrintHelp(cmdJobsCancel, &out)
	if !strings.Contains(out.String(), "--home DIR") {
		t.Fatalf("runs cancel help omits --home:\n%s", out.String())
	}
}
