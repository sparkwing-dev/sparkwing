package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func FuzzEngineTransitionPreservesSourceAttempt(f *testing.F) {
	f.Add(int64(0), true)
	f.Add(int64(1), true)
	f.Add(int64(0), false)
	f.Fuzz(func(t *testing.T, ordinal int64, present bool) {
		input := controllerwire.JobTransitionRequest{To: "running", Cause: "start"}
		if present {
			input.SourceAttempt = &ordinal
		}
		calls := 0
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			calls++
			var fields map[string]json.RawMessage
			if err := json.NewDecoder(req.Body).Decode(&fields); err != nil {
				t.Fatal(err)
			}
			raw, exists := fields["source_attempt"]
			if exists != present {
				t.Fatal("source attempt presence changed")
			}
			if present {
				var got int64
				if err := json.Unmarshal(raw, &got); err != nil || got != ordinal {
					t.Fatalf("source attempt changed: %s %v", raw, err)
				}
			}
			return engineResponse(200, `{"replayed":false,"cancel_requested":false,"state_seq":7,"generation":3,"attempt":4}`), nil
		})
		result, err := c.TransitionJob(t.Context(), "run", "job", input)
		if err != nil || result.Attempt != 4 || result.Generation != 3 || calls != 1 {
			t.Fatalf("execution ordinal lost: %+v %v calls=%d", result, err, calls)
		}
	})
}
