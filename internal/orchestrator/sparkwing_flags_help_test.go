package orchestrator

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintSparkwingFlagsSection_ContainsArcFlags pins per-pipeline
// help (`sparkwing run <pipeline> --help`) so it enumerates the
// sparkwing-owned flags. A future regression that drops one of
// these from sparkwing.SparkwingFlagDocs() fails this test loud.
func TestPrintSparkwingFlagsSection_ContainsArcFlags(t *testing.T) {
	var buf bytes.Buffer
	printSparkwingFlagsSection(&buf)
	out := buf.String()

	mustContain(t, out, "--sw-start-at")
	mustContain(t, out, "--sw-stop-at")
	mustContain(t, out, "--sw-dry-run")
	mustContain(t, out, "--sw-allow")

	mustContain(t, out, "--sw-profile")
	mustContain(t, out, "--sw-ref")

	mustContain(t, out, "SPARKWING FLAGS")
}

// TestPrintSparkwingFlagsSection_NoGroupHeaders pins that the
// pipeline-binary help renders flags as one flat list -- no group
// labels. Pipeline-author args (unprefixed) and sparkwing args
// (--sw-*) are visually separated by the SPARKWING FLAGS section
// header alone; further sub-grouping is noise.
func TestPrintSparkwingFlagsSection_NoGroupHeaders(t *testing.T) {
	var buf bytes.Buffer
	printSparkwingFlagsSection(&buf)
	out := buf.String()
	for _, label := range []string{"[System]", "[Source]", "[Range]", "[Safety]", "[Selection]", "[Other]"} {
		if strings.Contains(out, label) {
			t.Errorf("did not expect group label %q in flat output:\n%s", label, out)
		}
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q; got:\n%s", needle, haystack)
	}
}
