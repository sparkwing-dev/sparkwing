package controller

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

type sealedLogs struct{ storage.LogStore }

func (sealedLogs) NodeLogCompleteness(context.Context, string, string) (logseg.Completeness, bool, error) {
	return logseg.Completeness{State: logseg.StateComplete}, true, nil
}

func TestRecheckedReadsKeepLogCompleteness(t *testing.T) {
	var b backend.Backend = &recheckedEvents{Backend: backend.NewStoreBackend(nil, sealedLogs{}), recheck: func(context.Context) error { return nil }}
	verdict, err := backend.NodeLogCompleteness(t.Context(), b, "run", "quiet")
	if err != nil || verdict.State != logseg.StateComplete {
		t.Fatalf("reauthorized reader lost the sealed verdict: %+v %v", verdict, err)
	}
}
