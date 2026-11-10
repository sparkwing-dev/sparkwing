//go:build unix

package main

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRunReceiptBindsProducedOutputs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a CLI and runs a producer and consumer")
	}
	cli := buildSubmitCLI(t)
	user := t.TempDir()
	home := filepath.Join(user, "state")
	t.Setenv("HOME", user)
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(user, "missing.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(user, "secrets.key"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	offlineStopDaemon(t, home)
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: receipt\n    entrypoint: receipt\n", 0o600)
	writeSDKFixture(t, filepath.Join(repo, ".sparkwing"), `package main
import("context";"fmt";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type producer struct{sw.Produces[int]}
func(producer)Work(w *sw.Work)(*sw.WorkStep,error){return sw.Step(w,"produce",func(context.Context)(int,error){return 10000,nil}),nil}
type consumer struct{source sw.Ref[int]}
func(j consumer)Work(w *sw.Work)(*sw.WorkStep,error){return sw.Step(w,"verify",func(ctx context.Context)error{if got:=j.source.Get(ctx);got!=10000{return fmt.Errorf("output = %d",got)};return nil}),nil}
type pipeline struct{}
func(pipeline)Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{a:=sw.Job(p,"producer",producer{});sw.Job(p,"consumer",consumer{source:sw.RefTo[int](a)}).Needs(a);return nil}
func main(){sw.Register("receipt",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "receipt fixture")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "run", "receipt", "-C", repo, "--sw-route", "local", "--sw-no-update", "--sw-run-handle-file", filepath.Join(user, "handle.json"), "-o", "json")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, output)
	}
	var handle struct {
		RunID string `json:"run_id"`
	}
	data, err := os.ReadFile(filepath.Join(user, "handle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &handle); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(ctx, cli, "runs", "status", "--run", handle.RunID, "--view", "receipt", "-o", "json")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	var receipt controllerwire.Receipt
	if err := json.Unmarshal(output, &receipt); err != nil {
		t.Fatal(err)
	}
	persistence, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := persistence.Close(); err != nil {
			t.Error(err)
		}
	}()
	tenant, err := persistence.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := tenant.ListNodes(ctx, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, node := range nodes {
		if node.Outcome != "success" {
			t.Fatalf("job did not succeed: %+v", node)
		}
		if node.OutputDigest != "" {
			want[node.NodeID] = "sha256:" + node.OutputDigest
		}
	}
	if len(nodes) != 2 || want["producer"] == "" {
		t.Fatalf("missing executed producer/consumer outputs: %+v", nodes)
	}
	if !maps.Equal(receipt.Identity.OutputsHash, want) {
		t.Fatalf("receipt outputs_hash = %v, stored digests = %v", receipt.Identity.OutputsHash, want)
	}
}
