package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNativeChildOwnerBudgetFollowsAuthenticatedLocalPlacement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local bool
		zero  bool
	}{
		{name: "local", local: true},
		{name: "remote-local-hint"},
		{name: "remote-zero-refused", zero: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, handler, claim := childIntakeFixture(t)
			tenant, err := e.Store.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			parent, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			request := &parent.Request.Request
			request.Options, err = settings.ResolveIntake(runspec.Options{}, settings.OperatorInputs{}, settings.Context{Local: tc.local || tc.zero, NumCPU: 2})
			if err != nil {
				t.Fatal(err)
			}
			request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"deploy-kikd-prod"}}}
			request.Dispatch, request.Origin, request.ExecutionLocation = runspec.DispatchHost, runspec.OriginTrigger, "local"
			if tc.local {
				request.Origin = runspec.OriginLocal
				request.Source.Location = runspec.SourceLocation{LocalCheckout: t.TempDir(), PipelineDir: ".sparkwing"}
				principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim)))
				if err != nil {
					t.Fatal(err)
				}
				credential := principal.Capability
				credential.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "local:fixture"}
				raw, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: credential})
				if err != nil {
					t.Fatal(err)
				}
				engineSQL(t, e.Store, `UPDATE capabilities SET subject_kind=?,subject_id=?,authority_json=? WHERE prefix=?`, credential.Subject.Kind, credential.Subject.ID, raw, credential.Prefix)
			}
			raw, err := json.Marshal(parent.Request)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, raw)
			remote, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{handler}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := remote.Close(); err != nil {
					t.Error(err)
				}
			})
			principal, err := remote.Principal(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			process, err := remote.ProcessClient(t.Context(), principal, "job")
			if err != nil {
				t.Fatal(err)
			}
			id, err := process.StartChild(t.Context(), "deploy", nil, "", "", false)
			if tc.zero {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.BadRequest || !strings.Contains(failure.Message, "remote owner duration must be positive") {
					t.Fatalf("remote zero owner budget was not refused: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			child, err := tenant.ReadRun(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(child.Request.Request.Options.Requires.Value, []string{"deploy-kikd-prod"}) {
				t.Fatalf("child planning lost its owned-runner requirement: %+v", child.Request.Request.Options.Requires)
			}
			want := limits.RunDeadline
			if tc.local {
				want = 0
			}
			if got := child.Request.Request.Options.OwnerExecutionTimeout; !got.Set || got.Value != want {
				t.Fatalf("child owner budget = %+v, want %s", got, want)
			}
		})
	}
}
