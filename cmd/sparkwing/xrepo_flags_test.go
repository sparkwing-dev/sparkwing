package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestXrepoReportsFlagErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	for _, verb := range []string{"list", "add", "remove", "prune"} {
		t.Run(verb, func(t *testing.T) {
			cmd := outputContractCommand(t, "configure", "xrepo", verb, "--bogus")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err == nil || len(out) != 0 || !strings.Contains(stderr.String(), "unknown flag: --bogus") {
				t.Fatalf("flag error=%v stdout=%q stderr=%q", err, out, stderr.String())
			}
		})
	}
}
