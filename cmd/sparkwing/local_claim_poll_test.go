package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
)

type admitEveryRunner struct{}

func (admitEveryRunner) CheckRunner(context.Context, string) error { return nil }

// An advised idle poll left a new local run waiting up to 6.25 s for the
// runner's next claim, so the local controller must never widen the runner's cadence.
func TestLocalControllerNeverAdvisesAnIdleRunnerToPollLess(t *testing.T) {
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	var binding localcontroller.Binding
	owner, err := (localcontroller.Config{Bind: func(ctx context.Context, b localcontroller.Binding) (localcontroller.Services, error) {
		services, err := bindLocalController(ctx, b)
		binding = b
		return services, err
	}}).Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	binding.Controller.RunnerPolicy, binding.Controller.LocalRunnerPolicy = admitEveryRunner{}, admitEveryRunner{}
	subject, err := wingd.CurrentLocalSubject()
	if err != nil {
		t.Fatal(err)
	}
	identity := func(*http.Request) capability.Subject {
		return capability.Subject{Kind: capability.SubjectLocal, ID: subject}
	}
	serve := func(handler http.Handler) client.EngineOptions {
		return client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: queueHostTransport(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Result(), nil
		})}}
	}
	tokens, err := client.NewTokenManager(serve(newLocalServer(binding).WithPeerIdentity(identity).Handler()), false)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := tokens.CreateToken(t.Context(), controllerwire.TokenCreate{Scopes: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}})
	if err != nil {
		t.Fatal(err)
	}

	synctest.Test(t, func(t *testing.T) {
		options := serve(newLocalServer(binding).WithPeerIdentity(identity).Handler())
		options.Bearer = func(context.Context) (string, error) { return credential.Token, nil }
		queue, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		response, err := queue.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}})
		if err != nil || response.Claim != nil {
			t.Fatalf("empty local queue: %+v: %v", response, err)
		}
		if response.PollAfter != 0 {
			t.Fatalf("an hour-idle local controller advised polling every %s", response.PollAfter)
		}
	})
}
