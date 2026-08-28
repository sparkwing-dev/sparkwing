package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func decodeNDJSON[T any](t *testing.T, out string) []T {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var got []T
	for {
		var v T
		err := dec.Decode(&v)
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("decode NDJSON record %d: %v\noutput:\n%s", len(got), err, out)
		}
		got = append(got, v)
	}
}

func TestRunsListJSONIsNDJSON(t *testing.T) {
	out := seedRunsAndList(t, 6)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("runs list -o json emitted %d lines for 6 runs:\n%s", len(lines), out)
	}
	head := strings.Join(lines[:5], "\n") + "\n"
	five := decodeNDJSON[map[string]any](t, head)
	if len(five) != 5 {
		t.Fatalf("head -5 yielded %d records, want 5", len(five))
	}
	for i, r := range five {
		if r["id"] == nil || r["id"] == "" {
			t.Errorf("record %d has no id: %v", i, r)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Error("runs list -o json still opens with an array")
	}
}

func seedRunsAndList(t *testing.T, n int) string {
	t.Helper()
	p := newPaths(t)
	ctx := context.Background()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	started := time.Now().Add(-time.Hour)
	for i := range n {
		if err := st.CreateRun(ctx, store.Run{
			ID:        fmt.Sprintf("run-ndjson-%d", i),
			Pipeline:  "checks",
			Status:    "success",
			StartedAt: started.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var buf bytes.Buffer
	if err := orchestrator.ListJobs(ctx, p,
		orchestrator.ListOpts{JSON: true, Limit: n}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return buf.String()
}
