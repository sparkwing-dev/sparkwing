package orchestrator

import "testing"

// TestNodeFailureError_CountsCancellationsInsteadOfNamingThem pins the
// BW-1879 shape: a run that lost its daemon had 2 genuine failures and 72
// cancellations, and the old message named all 74 nodes as failed -- a
// two-node failure reading as a catastrophe.
func TestNodeFailureError_CountsCancellationsInsteadOfNamingThem(t *testing.T) {
	cancelled := make([]string, 72)
	for i := range cancelled {
		cancelled[i] = "n"
	}
	err := nodeFailureError([]string{"wingd", "compile"}, cancelled)
	want := "nodes failed: [wingd compile]; 72 more cancelled with the run"
	if err.Error() != want {
		t.Errorf("mixed outcome error = %q, want %q", err.Error(), want)
	}

	err = nodeFailureError([]string{"compile"}, nil)
	if got := err.Error(); got != "nodes failed: [compile]" {
		t.Errorf("failure-only error = %q, want the unchanged form", got)
	}

	err = nodeFailureError(nil, []string{"a", "b"})
	if got := err.Error(); got != "nodes cancelled: 2 node(s) stopped before completing" {
		t.Errorf("cancellation-only error = %q", got)
	}
}
