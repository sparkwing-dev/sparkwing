package orchestrator

import (
	"context"
	"strings"
	"testing"
)

type withChannel struct {
	Name string    `json:"name"`
	Done chan bool `json:"done"`
}

func TestNodeOutputMarshalError_CitesTheGuide(t *testing.T) {
	err := nodeOutputMarshalError("build", withChannel{}, context.Canceled)
	for _, want := range []string{"build", "orchestrator.withChannel", processBoundaryGuide} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
