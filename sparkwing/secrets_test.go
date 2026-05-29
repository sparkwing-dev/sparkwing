package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type secretsOnly struct{ Token string }

func TestPipelineSecrets_AccessorReturnsNilWhenAbsent(t *testing.T) {
	if got := sparkwing.PipelineSecrets[secretsOnly](context.Background()); got != nil {
		t.Errorf("absent secrets should return nil; got %+v", got)
	}
}
