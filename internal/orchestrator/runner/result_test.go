package runner

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNodeTerminal(t *testing.T) {
	cases := []struct {
		name string
		node *store.Node
		want bool
	}{
		{"nil", nil, false},
		{"pending", &store.Node{Status: "pending"}, false},
		{"running with no outcome", &store.Node{Status: "running"}, false},
		{"done with no outcome", &store.Node{Status: "done"}, false},
		{"done with outcome", &store.Node{Status: "done", Outcome: "success"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NodeTerminal(tc.node); got != tc.want {
				t.Fatalf("NodeTerminal = %v, want %v", got, tc.want)
			}
		})
	}
}

type outputsFunc func(ctx context.Context, runID, nodeID string) ([]byte, error)

func (f outputsFunc) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return f(ctx, runID, nodeID)
}

func TestResultFromNode_CarriesOutcomeErrorAndRawOutput(t *testing.T) {
	raw := []byte(`{"digest":"abc"}`)
	res := ResultFromNode(context.Background(), &store.Node{
		RunID:     "r",
		NodeID:    "n",
		Status:    "done",
		Outcome:   string(sparkwing.Failed),
		Error:     "boom",
		OutputRef: &store.OutputRef{Key: "outputs/r/n/a"},
	}, outputsFunc(func(_ context.Context, runID, nodeID string) ([]byte, error) {
		if runID != "r" || nodeID != "n" {
			return nil, store.ErrNotFound
		}
		return raw, nil
	}))
	if res.Outcome != sparkwing.Failed {
		t.Errorf("Outcome = %q", res.Outcome)
	}
	if res.Err == nil || res.Err.Error() != "boom" {
		t.Errorf("Err = %v, want boom", res.Err)
	}

	got, ok := res.Output.([]byte)
	if !ok {
		t.Fatalf("Output is %T, want []byte", res.Output)
	}
	if string(got) != string(raw) {
		t.Fatalf("Output = %s, want %s", got, raw)
	}
}

func TestResultFromNode_CleanSuccessHasNoErrorOrOutput(t *testing.T) {
	res := ResultFromNode(context.Background(), &store.Node{Status: "done", Outcome: string(sparkwing.Success)}, nil)
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.Output != nil {
		t.Errorf("Output = %v, want nil", res.Output)
	}
	if res.Usage != nil {
		t.Errorf("Usage = %v, want nil (the row carries no process accounting)", res.Usage)
	}
}
