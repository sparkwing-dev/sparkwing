package orchestrator

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintSparkwingFlagsSection_ContainsArcFlags(t *testing.T) {
	var buf bytes.Buffer
	printSparkwingFlagsSection(&buf)
	out := buf.String()

	mustContain(t, out, "--sw-start-at")
	mustContain(t, out, "--sw-stop-at")
	mustContain(t, out, "--sw-dry-run")
	mustContain(t, out, "--sw-allow")

	mustContain(t, out, "--profile")
	mustContain(t, out, "--target")
	mustContain(t, out, "--sw-ref")

	mustContain(t, out, "SPARKWING FLAGS")
}

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
