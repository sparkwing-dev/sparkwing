package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSealedSecretInputsNeverEscapeHostBoundaries(t *testing.T) {
	property := func(seed [24]byte) bool {
		value := "sealed-" + hex.EncodeToString(seed[:])
		for _, placement := range []string{"fresh", "resumed", "claimed"} {
			for _, scope := range []string{"pipeline", "job"} {
				t.Run(placement+"/"+scope, func(t *testing.T) {
					masks := NewMaskRegistry(secrets.NewMasker())
					var output strings.Builder
					logs := planningLogs{known: true, masker: masks.masker, sink: func(_ context.Context, frame nodeproto.FrameLog) error { output.WriteString(frame.Message); return nil }}
					acquire := func(ctx context.Context) (*source.Entry, error) {
						for _, stream := range []string{"stdout", "stderr"} {
							if err := logs.write(ctx, nodeproto.FrameLog{Level: "info", Event: stream, Message: value}); err != nil {
								t.Fatal(err)
							}
						}
						return nil, errors.New(value)
					}
					var err error
					if placement == "claimed" {
						config, input, state, _ := nativeHostFixture(t)
						config.Masks = masks
						spec := state.run.Spec
						spec.Request.Request.RequestedArgs = map[string]string{"token": value + "-requested"}
						spec.Request.Request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
						spec.Resolved.Args = map[string]string{"token": value}
						spec.Resolved.SecretArgs = []string{"token"}
						if scope == "job" {
							spec.Resolved.SecretArgs = []string{"default_token"}
							spec.Resolved.Args = map[string]string{"default_token": value}
						}
						input.Accepted.Spec, _ = json.Marshal(spec)
						_, claimed, _, buildErr := NewNativeHost(config)
						if buildErr != nil {
							t.Fatal(buildErr)
						}
						claimed.Acquire = func(ctx context.Context, _ source.Spec, _ source.Options) (*source.Entry, error) { return acquire(ctx) }
						err = claimed.Execute(t.Context(), input)
					} else {
						state, host, input := newOwnerFixture(t, placement == "resumed")
						host.Masks, host.Masker = masks, masks.masker
						request := &state.snapshot.Run.Request.Request
						request.RequestedArgs = map[string]string{"token": value}
						request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"token"}}
						input.SealedRequest, _ = json.Marshal(request)
						if state.snapshot.Run.Spec != nil {
							spec := state.snapshot.Run.Spec
							spec.Request = state.snapshot.Run.Request
							spec.Request.Request.SecretArgs = runspec.Optional[[]string]{}
							spec.Request.Request.RequestedArgs = nil
							spec.Resolved.Args = map[string]string{"default_token": value}
							spec.Resolved.SecretArgs = []string{"default_token"}
							state.snapshot.Run.Request = spec.Request
							input.SealedRequest, _ = json.Marshal(spec.Request.Request)
							accepted, acceptErr := ownerAccepted(state.snapshot.Run)
							if acceptErr != nil {
								t.Fatal(acceptErr)
							}
							input.Accepted = &accepted
						}
						host.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) { return acquire(ctx) }
						_, err = host.Run(t.Context(), input)
					}
					if err == nil {
						t.Fatal("acquisition failure disappeared")
					}
					if strings.Contains(output.String()+err.Error(), value) {
						t.Fatal("sealed secret escaped output or returned error")
					}
				})
			}
		}
		return !t.Failed()
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeRegistersTypedSecretListParts(t *testing.T) {
	masker := secrets.NewMasker()
	planner := SDKPlanner{Masker: masker, Masks: NewMaskRegistry(masker)}
	raw := json.RawMessage(`{"pipelines":[{"name":"list","args":[{"name":"tokens","type":"[]string","secret":true,"default":"default-first-secret,default-second-secret"},{"name":"token","type":"string","secret":true}]}]}`)
	_, complete, err := planner.describe(raw, PlanningProcessInput{Entrypoint: "list", Args: map[string]string{"tokens": "supplied-first-secret,supplied-second-secret", "token": "pw,1"}})
	if err != nil || !complete {
		t.Fatalf("describe complete=%v err=%v", complete, err)
	}
	if got := masker.Mask("default-first-secret default-second-secret supplied-first-secret supplied-second-secret pw,1 revision 1"); got != "*** *** *** *** *** revision 1" {
		t.Fatalf("list masking=%q", got)
	}
}
