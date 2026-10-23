//go:build unix

package nodehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestAnOutputHoldingASecretIsWithheldAndTheJobPasses(t *testing.T) {
	type deployment struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	for name, token := range map[string]string{
		"plain":   splitSecret,
		"base64":  base64.StdEncoding.EncodeToString([]byte(splitSecret)),
		"escaped": splitSecret,
	} {
		t.Run(name, func(t *testing.T) {
			f, input, deps := maskingExecutorFixture(t)
			output, err := json.Marshal(deployment{URL: "https://example.test", Token: token})
			if err != nil {
				t.Fatal(err)
			}
			if name == "escaped" {
				output = []byte(strings.ReplaceAll(string(output), "-", `\u002d`))
				if strings.Contains(string(output), splitSecret) {
					t.Fatal("escaped fixture still spells the secret plainly")
				}
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, activity)
				process.(*executorProcess).result.Output = output
				return process, err
			}
			deps.PublishOutput = func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error) {
				t.Error("an output holding a secret was published")
				return nil, nil
			}
			var mu sync.Mutex
			var annotations []nodeproto.AnnotationRequest
			deps.Annotate = func(_ context.Context, _ NodeInput, body nodeproto.AnnotationRequest) error {
				mu.Lock()
				defer mu.Unlock()
				annotations = append(annotations, body)
				return nil
			}
			var logs strings.Builder
			deps.Log = func(_ context.Context, _ NodeInput, frame nodeproto.FrameLog) error {
				mu.Lock()
				defer mu.Unlock()
				raw, _ := json.Marshal(frame)
				logs.Write(raw)
				return nil
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err := host.Execute(t.Context(), input); err != nil {
				t.Fatalf("job failed: %v", err)
			}
			row, err := f.Tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)})
			if err != nil {
				t.Fatal(err)
			}
			if row.Outcome != runstate.NodeSuccess || row.OutputRecord != nil {
				t.Fatalf("row outcome %q output %v, want success with no output", row.Outcome, row.OutputRecord)
			}
			if len(annotations) != 1 || annotations[0].Level != "warn" || annotations[0].Message != "output withheld: contains a secret" {
				t.Fatalf("annotations = %+v", annotations)
			}
			if !strings.Contains(logs.String(), "output withheld: contains a secret") || strings.Contains(logs.String(), token) {
				t.Fatalf("logs = %s", logs.String())
			}
		})
	}
}
