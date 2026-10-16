package controller

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func FuzzReceiptSnapshotPrivateInputsHaveNoHash(f *testing.F) {
	f.Add("token", "private-value", false, false)
	f.Add("token", "private-value", true, false)
	f.Add("token", "private-value", true, true)
	f.Fuzz(func(t *testing.T, name, value string, classified, accepted bool) {
		request := runspec.SealedRequest{Request: runspec.Request{
			RequestedArgs: map[string]string{name: value},
			SecretArgs:    runspec.Optional[[]string]{Set: classified, Value: []string{name}},
		}}
		snapshot := store.RunSnapshot{Run: store.RunRow{Request: request}}
		if accepted {
			snapshot.Run.Spec = &runspec.Spec{Request: request, Resolved: runspec.Resolved{
				Args: map[string]string{name: value}, SecretArgs: []string{name},
			}}
		}
		run, nodes, err := receiptSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		got := receipt.BuildReceipt(run, nodes, 0, "")
		if got.Identity.InputsHash != "" {
			t.Fatal("receipt published a hash of private inputs")
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["invocation"] != nil || fields["store"] != nil || bytes.Contains(raw, []byte(`"node_id"`)) {
			t.Fatalf("receipt exposed fields outside the shared projection: %s", raw)
		}
	})
}
