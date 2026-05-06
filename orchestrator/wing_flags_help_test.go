package orchestrator

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintWingFlagsSection_ContainsIMPArcFlags pins IMP-039:
// per-pipeline help (`wing <pipeline> --help`,
// `sparkwing run <pipeline> --help`) must enumerate the IMP-007/014/015
// wing flags. Pre-fix the footer was a hand-coded "(--on, --from,
// --config)" line that omitted --start-at, --stop-at, --dry-run, and
// the --allow-* set entirely. A future regression that drops one of
// these from sparkwing.WingFlagDocs() fails this test loud.
func TestPrintWingFlagsSection_ContainsIMPArcFlags(t *testing.T) {
	var buf bytes.Buffer
	printWingFlagsSection(&buf)
	out := buf.String()

	// IMP-007: range-resume.
	mustContain(t, out, "--start-at")
	mustContain(t, out, "--stop-at")
	// IMP-014: dry-run.
	mustContain(t, out, "--dry-run")
	// IMP-015: blast-radius escape hatches.
	mustContain(t, out, "--allow-destructive")
	mustContain(t, out, "--allow-prod")
	mustContain(t, out, "--allow-money")

	// Pre-IMP-039 staples must still appear -- the regression we want
	// to avoid is REPLACING the old hand-coded list with an equally
	// stale newer one.
	mustContain(t, out, "--on")
	mustContain(t, out, "--from")
	mustContain(t, out, "--config")
	mustContain(t, out, "--retry-of")

	// The header label keeps the section discoverable.
	mustContain(t, out, "WING FLAGS")
}

// TestPrintWingFlagsSection_GroupsRender pins the section structure:
// each Group from WingFlagDocs gets its own header so the reader can
// tell --start-at apart from --on at a glance.
func TestPrintWingFlagsSection_GroupsRender(t *testing.T) {
	var buf bytes.Buffer
	printWingFlagsSection(&buf)
	out := buf.String()
	for _, label := range []string{"[Source]", "[Range]", "[Safety]", "[System]"} {
		if !strings.Contains(out, label) {
			t.Errorf("expected group label %q in output:\n%s", label, out)
		}
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q; got:\n%s", needle, haystack)
	}
}
