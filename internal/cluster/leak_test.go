package cluster

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := RunAgentCLI(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// hack: a pooled node runs `<binary> run-node <run> <node>` and here the binary is
	// this test binary, so that argv is the pipeline child's turn.
	if len(os.Args) == 4 && os.Args[1] == "run-node" {
		os.Exit(runPipelineChildForTest(os.Args[2], os.Args[3]))
	}
	testleak.Main(m)
}
