package store

import (
	"testing"
)

// The agent label is self-declared, so every cell of the trust matrix (trust class, proven
// repository, pipeline and shared secrets, OIDC claims, cache scope, source credentials and
// claim eligibility on every host shape) must read the same with and without it.
func TestAgentLabelLeavesEveryAuthorityCellUnchanged(t *testing.T) {
	plain := renderTrustMatrix(t, trustMatrixRows())
	labeled := trustMatrixRows()
	for i := range labeled {
		labeled[i].request.Request.Agent = "claude-code:land-agent"
	}
	if diff := matrixDiff(plain, renderTrustMatrix(t, labeled)); diff != "" {
		t.Fatalf("an agent label changed authority (- without, + with):\n%s", diff)
	}
}
