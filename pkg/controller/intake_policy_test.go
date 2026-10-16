package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestControllerIntakeSealsPersistedPolicyOnce(t *testing.T) {
	server, session := credentialSessionServer(t)
	st := server.store
	engineSQL(t, st, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	tenant, err := st.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	input := store.IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, IdempotencyKey: "same", Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginTrigger,
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}},
		Options: runspec.Options{Priority: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}},
	}}
	setSeconds := func(seconds int64) {
		t.Helper()
		if _, err := st.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitRunSeconds: seconds}); err != nil {
			t.Fatal(err)
		}
	}
	setSeconds(3600)
	first, err := tenant.AdmitRun(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Request.Ceilings.OwnerExecutionTimeout.Value != time.Hour || first.Request.Request.Options.OwnerExecutionTimeout.Value != time.Hour || !first.Request.Request.Options.Priority.Set || first.Request.Request.Options.Priority.Value != 0 {
		t.Fatalf("settings were not resolved: %+v", first.Request)
	}
	setSeconds(7200)
	replay, err := tenant.AdmitRun(t.Context(), input)
	if err != nil || !replay.Replayed || replay.Request.Hash != first.Request.Hash || replay.Request.Ceilings.OwnerExecutionTimeout.Value != time.Hour {
		t.Fatalf("policy changed on replay: %+v, %v", replay, err)
	}
	input.IdempotencyKey = "next"
	next, err := tenant.AdmitRun(t.Context(), input)
	if err != nil || next.Request.Ceilings.OwnerExecutionTimeout.Value != 2*time.Hour {
		t.Fatalf("fresh intake ignored operator update: %+v, %v", next, err)
	}
	input.IdempotencyKey = "refused"
	input.Request.Options.OwnerExecutionTimeout = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: 3 * time.Hour}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "owner_execution"}}
	if _, err := tenant.AdmitRun(t.Context(), input); err == nil {
		t.Fatal("request bypassed remote operator ceiling")
	}
	input.Request.Origin = runspec.OriginLocal
	input.Request.Options.OwnerExecutionTimeout.Value = time.Hour
	if _, err := tenant.AdmitRun(t.Context(), input); err == nil {
		t.Fatal("remote principal asserted executable local provenance")
	}
}

func TestControllerIntakeRefusesUnsafeAcquisitionLocations(t *testing.T) {
	server, session := credentialSessionServer(t)
	engineSQL(t, server.store, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	tenant, err := server.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	admit := func(location runspec.SourceLocation) error {
		_, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Request: runspec.Request{
			Version: 1, Team: "a", Pipeline: "ci", Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginTrigger,
			Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}}, Source: runspec.Source{Location: location},
		}})
		return err
	}
	for _, location := range []runspec.SourceLocation{
		{RemoteURL: "https://user:private@example.com/acme/repo", PipelineDir: ".sparkwing"},
		{RemoteURL: "https://example.com/acme/repo?token=private", PipelineDir: ".sparkwing"},
		{RemoteURL: "https://example.com/acme/repo", PipelineDir: "../outside"},
		{RemoteURL: "https://example.com/acme/repo", PipelineDir: "/outside"},
		{LocalCheckout: "/host", PipelineDir: ".sparkwing"},
	} {
		err := admit(location)
		if err == nil {
			t.Fatal("unsafe acquisition location admitted")
		}
	}
	if err := quick.Check(func(password string) bool {
		remote := url.URL{Scheme: "https", Host: "example.com", Path: "/acme/repo", User: url.UserPassword("user", password)}
		return admit(runspec.SourceLocation{RemoteURL: remote.String(), PipelineDir: ".sparkwing"}) != nil
	}, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal("credential-bearing acquisition URL admitted")
	}
}

func TestControllerIntakePublishesCapturedLocalHistory(t *testing.T) {
	server, session := credentialSessionServer(t)
	engineSQL(t, server.store, `UPDATE teams SET next_run_number=2 WHERE name='a'`)
	tenant, err := server.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	admitted, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Request: runspec.Request{
		Version: 1, Team: "a", Pipeline: "historical", Mode: runspec.IntakePublish, Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}},
		Options: runspec.Options{OwnerExecutionTimeout: runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "owner_execution"}}},
	}})
	if err != nil || admitted.OwnerUnit != nil || admitted.Request.Request.Options.OwnerExecutionTimeout.Value != 0 || admitted.Request.Request.Origin != runspec.OriginPublish {
		t.Fatalf("local history gained an execution deadline or owner: %+v, %v", admitted, err)
	}
	_ = engineHTTPRoot(t, &EngineController{Store: server.store}, "a", "publisher", []capability.Grant{capability.DataWrite})
	authority := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "publisher"}
	principal, err := server.store.ResolveAuthority(t.Context(), authority)
	if err != nil {
		t.Fatal(err)
	}
	credential := principal.Capability
	credential.Binding = capability.Binding{Kind: capability.BindingRun, RunID: admitted.RunID}
	credential.Data = &capability.DataScope{Use: capability.DataPublish, RunID: admitted.RunID, RepositoryKey: "git:example.com/acme/repo"}
	encoded, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: credential})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.store, `UPDATE capabilities SET bind_kind='run',bind_run=?,repo_key=?,authority_json=? WHERE prefix='publisher'`, admitted.RunID, credential.Data.RepositoryKey, encoded)
	timeout := int64(12 * time.Hour / time.Millisecond)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "historical", Inline: true, TimeoutMS: &timeout, Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := tenant.AcceptPlan(t.Context(), store.PlanAcceptance{RunID: admitted.RunID, Actor: store.Actor{Kind: store.ActorOperator, Authority: authority, CapabilityPrefix: credential.Prefix}, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil || accepted.Owner != nil || accepted.Spec.Resolved.Settings.Jobs["historical"].Timeout.Value != 12*time.Hour {
		t.Fatalf("historical plan gained remote execution bounds: %v", err)
	}
}
