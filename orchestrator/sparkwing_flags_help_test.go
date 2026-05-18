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
	mustContain(t, out, "--sw-allow-destructive")
	mustContain(t, out, "--sw-allow-prod")
	mustContain(t, out, "--sw-allow-money")

	mustContain(t, out, "--sw-on")
	mustContain(t, out, "--sw-from")
	mustContain(t, out, "--sw-retry-of")

	mustContain(t, out, "SPARKWING FLAGS")
}

// TestPrintSparkwingFlagsSection_GroupsRender pins that every
// sparkwing-owned flag renders under a single [System] group header.
// Pipeline-author flag namespace is fully unprefixed and sparkwing
// flags live under one unified [System] label so the operator only
// sees two top-level groupings: pipeline args (unprefixed) and
// sparkwing args (--sw-* under System).
func TestPrintSparkwingFlagsSection_GroupsRender(t *testing.T) {
	var buf bytes.Buffer
	printSparkwingFlagsSection(&buf)
	out := buf.String()
	if !strings.Contains(out, "[System]") {
		t.Errorf("expected group label [System] in output:\n%s", out)
	}
	for _, label := range []string{"[Source]", "[Range]", "[Safety]", "[Selection]"} {
		if strings.Contains(out, label) {
			t.Errorf("did not expect sub-group label %q in output (collapsed under [System]):\n%s", label, out)
		}
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q; got:\n%s", needle, haystack)
	}
}
