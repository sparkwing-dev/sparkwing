package orchestrator

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAnAwaitedChildWithoutAParentLeaseIsReported(t *testing.T) {
	awaited := &store.Trigger{ID: "child", ParentRunID: "parent-run", TriggerSource: "await-pipeline"}
	for _, tc := range []struct {
		name        string
		trigger     *store.Trigger
		token       string
		coordinated bool
		want        bool
	}{
		{"claimed without the lease", awaited, "", true, true},
		{"attached", awaited, "lease", true, false},
		{"no daemon to attach through", awaited, "", false, false},
		{"not awaited", &store.Trigger{ID: "run", TriggerSource: "manual"}, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			warnUnattachedChild(slog.New(slog.NewTextHandler(&buf, nil)), tc.trigger, tc.token, tc.coordinated)
			got := strings.Contains(buf.String(), "parent_run_id=parent-run")
			if got != tc.want {
				t.Errorf("warned=%v, want %v: %s", got, tc.want, buf.String())
			}
		})
	}
}
