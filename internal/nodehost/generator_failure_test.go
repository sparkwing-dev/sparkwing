package nodehost

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestGeneratorFailureWithholdsUnknownSecretDetails(t *testing.T) {
	for _, transport := range []bool{false, true} {
		process := &executorProcess{answer: func(context.Context, nodeproto.Request) (any, error) {
			if transport {
				return nil, errors.New("unknown-generator-secret")
			}
			return nodeproto.EvalResult{Error: &apierr.Error{Code: apierr.SDKError, Message: "unknown-generator-secret"}}, nil
		}}
		session := &executorSession{process: process}
		value, err := (&sdkCode{}).eval(t.Context(), session, NodeInput{}, "generate-closure", "generate", nil, nil)
		if len(value) != 0 || err == nil || err.Error() != generatorFailureMessage {
			t.Fatalf("transport=%v value=%s error=%v", transport, value, err)
		}
	}
}
