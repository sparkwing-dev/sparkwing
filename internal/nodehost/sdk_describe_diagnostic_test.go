package nodehost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func FuzzSDKDescribeFailureWithholdsDetailsAndNamesClass(f *testing.F) {
	f.Add("unknown-secret", uint8(0))
	f.Add("\x00\nprivate", uint8(1))
	f.Add("sdk_exited", uint8(2))
	f.Add("protocol_mismatch", uint8(3))
	f.Add("private-code", uint8(4))
	f.Fuzz(func(t *testing.T, secret string, mode uint8) {
		p, input, fixture, _ := sdkPlanningFixture(t)
		code := apierr.SDKError
		var cause error
		switch mode % 5 {
		case 0:
			cause = errors.New(secret)
		case 1:
			code, cause = apierr.DeadlineExceeded, fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)
		case 2:
			code = apierr.SDKExited
		case 3:
			code = apierr.ProtocolMismatch
		case 4:
			cause = &apierr.Error{Code: apierr.Code("unregistered-" + secret), Message: secret}
		}
		if cause == nil {
			cause = &apierr.Error{Code: code, Message: secret, Hint: secret, Details: map[string]any{secret: secret}}
		}
		p.Start = func(context.Context, PlanningProcessInput, func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
			return nil, cause
		}
		_, err := p.Prepare(t.Context(), input, fixture.input)
		var failure *apierr.Error
		want := "describe failed: " + string(code) + " (details withheld: secrets not yet known)"
		if !errors.As(err, &failure) || failure.Code != code || failure.Message != want || len(failure.Details) != 0 {
			t.Fatalf("describe diagnostic = %v; want %q (%s)", err, want, code)
		}
		wantHint := ""
		if code == apierr.ProtocolMismatch {
			wantHint = "Rebuild .sparkwing with the matching SDK."
		}
		if failure.Hint != wantHint || err.Error() != want {
			t.Fatalf("describe leaked untrusted fields: %+v", failure)
		}
	})
}
