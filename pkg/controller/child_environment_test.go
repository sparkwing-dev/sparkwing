package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestNativeChildIntakeInheritsCapturedEnvironment(t *testing.T) {
	property := func(seed uint32, descendant bool) bool {
		e, handler, claim := childIntakeFixture(t)
		tenant, err := e.Store.ForTeam(t.Context(), "a")
		if err != nil {
			t.Fatal(err)
		}
		parent, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{"APP_EMPTY": "", "APP_VALUE": string(rune('a' + seed%26))}
		parent.Request.Request.Env, err = runspec.EnvironmentDigest(values, strings.Repeat("5", 64))
		if err != nil {
			t.Fatal(err)
		}
		parent.Request.Request.EnvironmentSource = runspec.EnvironmentCaptured
		parent.Request.Request.Origin = runspec.OriginLocal
		if descendant {
			parent.Request.Request.Origin = runspec.OriginChild
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
		defer func() {
			if err := remote.Close(); err != nil {
				t.Error(err)
			}
		}()
		principal, err := remote.Principal(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		process, err := remote.ProcessClient(t.Context(), principal, "job")
		if err != nil {
			t.Fatal(err)
		}
		id, err := process.StartChild(t.Context(), "deploy", nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		child, err := tenant.ReadRun(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		bytes := map[string][]byte{}
		for name, value := range values {
			bytes[name] = []byte(value)
		}
		if err := runspec.VerifyEnvironment(child.Request.Request.Env, bytes); err != nil || child.Request.Request.Origin != runspec.OriginChild {
			t.Fatalf("child lost captured environment: %+v %v", child.Request.Request, err)
		}
		if child.Request.Request.EnvironmentSource != runspec.EnvironmentCaptured {
			t.Fatal("child lost environment provenance")
		}
		var encrypted string
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT submitted_env FROM runs WHERE team='a' AND id=?`, id).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		forged := parent.Request.Request
		forged.Pipeline, forged.Origin = "deploy", runspec.OriginChild
		forged.Lineage = runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}
		if descendant {
			forged.EnvironmentSource = runspec.EnvironmentSubmitted
		} else {
			forged.Env.Digest = "changed"
		}
		attack, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: "forged", Request: forged})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, handler, claim, "POST", "/api/v1/runs/run/children", string(attack), 409)
		return encrypted == ""
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}
