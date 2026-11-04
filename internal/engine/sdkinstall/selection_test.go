package sdkinstall

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestSelectionInputsChangeWithTheModuleFilesItReads(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, at time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-time.Hour)
	write("go.mod", "module example.com/pipeline\n", base)
	environment := procenv.Env{}
	args, before := selectionInputs(dir, environment)
	if again, same := selectionInputs(dir, environment); same != before || len(again) != len(args) {
		t.Fatal("unchanged module files changed the selection inputs")
	}
	write("go.sum", "example.com/dep v1.0.0 h1:x\n", base)
	if _, after := selectionInputs(dir, environment); after == before {
		t.Fatal("a new go.sum left the selection inputs unchanged")
	}
	_, before = selectionInputs(dir, environment)
	write("go.mod", "module example.com/pipeline\n\nrequire example.com/dep v1.0.0\n", base.Add(time.Second))
	if _, after := selectionInputs(dir, environment); after == before {
		t.Fatal("a rewritten go.mod left the selection inputs unchanged")
	}
}
