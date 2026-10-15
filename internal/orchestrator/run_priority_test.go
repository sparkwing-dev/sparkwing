package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestResolveRunPriority_LiteralIntegerNeverAsksTheQueue(t *testing.T) {
	// safety: Home names a directory with no daemon, so a query would fail;
	// a literal value must not make one.
	admission := testWingdAdmission(t.TempDir(), nil)
	for _, tc := range []struct {
		raw  string
		want int
	}{{"42", 42}, {"0", 0}, {"-9", -9}, {" 7 ", 7}} {
		got, set, err := resolveRunPriority(context.Background(), tc.raw, admission)
		if err != nil {
			t.Fatalf("resolveRunPriority(%q) = %v", tc.raw, err)
		}
		if !set || got.value != tc.want || got.source != "flag" {
			t.Errorf("resolveRunPriority(%q) = %+v set=%v, want %d/flag", tc.raw, got, set, tc.want)
		}
	}
}

func TestResolveRunPriority_EmptyRequestLeavesThePlanAlone(t *testing.T) {
	got, set, err := resolveRunPriority(context.Background(), "  ", testWingdAdmission(t.TempDir(), nil))
	if err != nil || set || got.source != "" {
		t.Fatalf("resolveRunPriority(blank) = %+v set=%v err=%v, want an untouched plan", got, set, err)
	}
}

// safety: an absent daemon is an empty queue rather than a failure, so a
// priority request never blocks a run that reaches no daemon at all.
func TestResolveRunPriority_NoDaemonIsAnEmptyQueue(t *testing.T) {
	admission := testWingdAdmission(t.TempDir(), nil)
	for _, tc := range []struct {
		mode string
		want int
	}{{"front", 1}, {"back", -1}} {
		got, set, err := resolveRunPriority(context.Background(), tc.mode, admission)
		if err != nil {
			t.Fatalf("resolveRunPriority(%q) = %v", tc.mode, err)
		}
		if !set || got.value != tc.want || got.source != tc.mode {
			t.Errorf("resolveRunPriority(%q) = %+v, want %d/%s", tc.mode, got, tc.want, tc.mode)
		}
	}
}

func TestResolveRunPriority_RejectsAnUnusableValue(t *testing.T) {
	_, _, err := resolveRunPriority(context.Background(), "sideways", testWingdAdmission(t.TempDir(), nil))
	if err == nil {
		t.Fatal("an unusable priority was accepted")
	}
	for _, want := range []string{"--sw-priority", "front", "back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
