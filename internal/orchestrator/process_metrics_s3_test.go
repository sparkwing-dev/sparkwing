package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type rejectUnknownMetric struct{ *client.Client }

func (c rejectUnknownMetric) AddNodeMetricSample(ctx context.Context, run, node string, s store.MetricSample) error {
	if s.Kind == store.MetricUnknown {
		return errors.New("unknown metric rejected")
	}
	return c.Client.AddNodeMetricSample(ctx, run, node, s)
}

func TestS3CoordinatedExecutionRequiresUnknownAccounting(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "rejected"}[reject], func(t *testing.T) {
			ctx := withProcessNode(withLocalExecution(t.Context()), "run", "build")
			t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
			art := newMemBucket()
			state := s3state.New(art)
			t.Cleanup(func() { _ = state.Close() })
			if err := state.CreateRun(ctx, store.Run{ID: "run", Pipeline: "metrics", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := state.CreateNode(ctx, store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			backend := S3Backends(nil, state, art)
			loopback, err := startRunLoopback(ctx, &Options{State: state, ArtifactStore: art, RunID: "run"}, backend, quietTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(loopback.Close)
			c := client.NewWithToken(loopback.url, nil, loopback.token)
			if err := c.AcknowledgeNodeExecutionStart(ctx, "run", "build", store.ExecutionStart{AttemptOrdinal: 1, ExecutorKind: store.ExecutorKindLocal, ExecutorID: "local"}); err == nil {
				t.Fatal("shim unexpectedly supports execution acknowledgement")
			}
			backends := RemoteBackends(context.Background(), c, localLogs{paths: newInternalPaths(t)}, art, nil, 0)
			if reject {
				backends.State = rejectUnknownMetric{c}
			}
			bodies := 0
			node := sparkwing.Job(sparkwing.NewPlan(), "build", func(context.Context) error { bodies++; return nil })
			result := NewNodeExecutor(backends).executeCoordinated(ctx, runner.Request{RunID: "run", NodeID: "build", Node: node})
			if reject {
				if result.Err == nil || bodies != 0 {
					t.Fatalf("rejected marker execution=%+v bodies=%d", result, bodies)
				}
				return
			}
			if result.Err != nil || !result.Outcome.OK() || bodies != 1 {
				t.Fatalf("execution=%+v bodies=%d", result, bodies)
			}
			if err := state.FinishRun(ctx, "run", "success", ""); err != nil {
				t.Fatal(err)
			}
			reader, err := art.Get(ctx, "runs/run/state.ndjson")
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			decoder := json.NewDecoder(reader)
			unknown := false
			for {
				var envelope struct {
					Kind string
					Data json.RawMessage
				}
				err := decoder.Decode(&envelope)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if envelope.Kind == s3state.KindMetricSample {
					var data struct {
						NodeID string             `json:"node_id"`
						Sample store.MetricSample `json:"sample"`
					}
					if err := json.Unmarshal(envelope.Data, &data); err != nil {
						t.Fatal(err)
					}
					if data.NodeID == "build" && data.Sample.Kind == store.MetricUnknown {
						unknown = true
					}
				}
			}
			if !unknown {
				t.Fatal("unattributed execution has no archived unknown marker")
			}
			restored := s3state.New(art)
			t.Cleanup(func() { _ = restored.Close() })
			stored, err := restored.GetNode(ctx, "run", "build")
			if err != nil || stored == nil || stored.Outcome != "success" {
				t.Fatalf("archived node=%+v error=%v", stored, err)
			}
		})
	}
}
