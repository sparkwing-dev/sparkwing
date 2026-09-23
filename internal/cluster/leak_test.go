package cluster

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	// A pooled node runs `<binary> run-node <run> <node>`, and in these tests
	// the binary is this one, so that argv is the pipeline child's turn.
	if len(os.Args) == 4 && os.Args[1] == "run-node" {
		os.Exit(runPipelineChildForTest(os.Args[2], os.Args[3]))
	}
	testleak.Main(m)
}
