package sparkwing

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type oidcResultServices struct {
	sdkserve.Services
	err error
}

func (s oidcResultServices) OIDC(context.Context, string) (string, error) { return "", s.err }

func TestProtocolOIDCUnavailableSentinelPreservesWireFailure(t *testing.T) {
	for _, code := range []apierr.Code{apierr.NotFound, apierr.Forbidden, apierr.Internal} {
		t.Run(string(code), func(t *testing.T) {
			failure := &apierr.Error{Code: code, Message: "OIDC test failure"}
			ctx := (sdkOperations{}).Context(t.Context(), nodeproto.RunContext{}, nodeproto.RunnerIdentity{}, oidcResultServices{err: failure})
			_, err := OIDCToken(ctx, "example")
			if errors.Is(err, ErrOIDCUnavailable) != (code == apierr.NotFound) {
				t.Fatalf("unavailable classification: %v", err)
			}
			var wire *apierr.Error
			if !errors.As(err, &wire) || wire != failure {
				t.Fatalf("wire failure was lost: %v", err)
			}
		})
	}
}
