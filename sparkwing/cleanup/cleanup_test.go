package cleanup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func TestRegisterWritesACommandRecordAndReleaseRemovesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-1")
	t.Setenv("SPARKWING_NODE_ID", "build")
	ledger := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir())

	release, err := Register(context.Background(), Spec{Argv: []string{"docker", "rm", "-f", "c1"}, Description: "docker run x"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ledger.List()
	if len(got) != 1 || got[0].Handle.Kind != "command" || got[0].Command != "docker run x" {
		t.Fatalf("record = %+v", got)
	}
	if got[0].Handle.Argv[0] != "docker" || got[0].Handle.ID == "" {
		t.Fatalf("handle = %+v", got[0].Handle)
	}
	release()
	if got, _ := ledger.List(); len(got) != 0 {
		t.Fatalf("release did not remove the record: %+v", got)
	}
}

func TestRegisterOutsideARunIsANoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "")
	t.Setenv("SPARKWING_NODE_ID", "")
	release, err := Register(context.Background(), Spec{Argv: []string{"docker", "rm", "-f", "c1"}})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if got, _ := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir()).List(); len(got) != 0 {
		t.Fatalf("registered a cleanup outside a run: %+v", got)
	}
}

func TestRegisterWithNoArgvIsANoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-1")
	t.Setenv("SPARKWING_NODE_ID", "build")
	if _, err := Register(context.Background(), Spec{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := sessionledger.Open(filepath.Join(home, "sessions")).List(); len(got) != 0 {
		t.Fatalf("empty spec registered something: %+v", got)
	}
}
