package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestBurstAcceptanceLocalOwnerPersonalWorkerAndCancellation(t *testing.T) {
	burstAcceptanceFlow(t, false, "personal")
}

func TestBurstAcceptanceSaturatedLocalOwnerPersonalWorker(t *testing.T) {
	burstAcceptanceFlow(t, true, "personal")
}

func TestBurstAcceptanceSaturatedLocalOwnerCloudWorker(t *testing.T) {
	burstAcceptanceFlow(t, true, "cloud")
}

func TestBurstAcceptanceSaturatedLocalOwnerCloudRetryWorker(t *testing.T) {
	burstAcceptanceFlow(t, true, "cloud-retry")
}

func TestBurstAcceptanceSelfClaimUsesPrivateReservationWithZeroFreeCapacity(t *testing.T) {
	burstAcceptanceFlow(t, false, "here-reserved")
}

func TestBurstAcceptanceLocalOwnerExecutesBeforeOverflow(t *testing.T) {
	burstAcceptanceFlow(t, false, "here")
}

func burstAcceptanceFlow(t *testing.T, saturated bool, placement string) {
	burstAcceptanceExecution(t, saturated, placement, nil)
}

// BurstExecutionProof binds real SDK execution to the composed burst intake and claim fixture.
type BurstExecutionProof struct {
	Secret        string
	Pipeline      string
	Plan          nodeproto.PlanDoc
	Engine        engine.Identity
	Source        func(*testing.T) (string, string)
	Execute       func(client.EngineOptions, controllerwire.ClaimedUnit, controllerwire.Controller)
	ClaimCloud    func(client.EngineOptions) controllerwire.ClaimedUnit
	ExchangeCloud func(client.EngineOptions, controllerwire.ClaimedUnit) controllerwire.ClaimedUnit
	FinishCloud   func()
}

// BurstExecutionAcceptance runs the shared burst path through the supplied executor.
func BurstExecutionAcceptance(t *testing.T, placement string, proof BurstExecutionProof) {
	t.Helper()
	burstAcceptanceExecution(t, placement != "here", placement, &proof)
}

func burstAcceptanceExecution(t *testing.T, saturated bool, placement string, proof *BurstExecutionProof) {
	t.Helper()
	cloudWorker := strings.HasPrefix(placement, "cloud")
	clock := &testClock{}
	s, options, input, memberBearer := sourceUploadFixture(t, clock.option())
	engineSQL(t, s.store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp' WHERE name='a'`)
	engineSQL(t, s.store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
	now := time.Now().UTC()
	clock.pin(func() time.Time { return now })
	s.store.SetClaimProfileResolver(billingCloudProfile{})
	e := &EngineController{Store: s.store, RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })}
	s.WithEngineController(e)
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	newClient := func(raw string, launcher bool) controllerwire.Controller {
		t.Helper()
		connection := options
		connection.Bearer = func(context.Context) (string, error) { return raw, nil }
		connection.Launcher = launcher
		c, err := client.NewEngineClient(connection)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	member := newClient(memberBearer, false)
	initialOverflow := sourceUploadHTTP(s.Handler(), memberBearer, "GET", "/api/v1/overflow", "")
	var overflow controllerwire.OverflowControl
	if initialOverflow.Code != http.StatusOK || json.Unmarshal(initialOverflow.Body.Bytes(), &overflow) != nil || overflow.MonthlyMinutes != nil {
		t.Fatal("requester's overflow default is capped")
	}
	personal := burstAcceptancePersonalMachine(t, s)
	worker := newClient(personal, false)
	cloudBearer := engineHTTPRoot(t, e, "", "burst-launcher", []capability.Grant{capability.ClaimsLaunch})
	cloud := newClient(cloudBearer, true)
	poll := controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitNode}, AvailableCores: 64}
	if observed, err := worker.Claim(t.Context(), poll); err != nil || observed.Claim != nil {
		t.Fatalf("personal machine observation: %+v %v", observed, err)
	}

	bundle := filepath.Join(t.TempDir(), "snapshot.bundle")
	repo, sha := sourcetest.Repository(t)
	if proof != nil && proof.Source != nil {
		repo, sha = proof.Source(t)
	}
	sourcetest.Git(t, repo, "bundle", "create", bundle, "HEAD")
	input.Git.SHA, input.Source.Ref = sha, sha
	input.Git.Repository.Display = "https://example.test/acme/repo"
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: bundle, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	input.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here", Hold: 2 * time.Second}, {Target: "machines", Hold: 2 * time.Second}, {Target: "cloud"}}}
	input.LocalSecretNames = []string{"TOKEN"}
	if proof != nil {
		input.Pipeline = proof.Pipeline
		input.Source.Entrypoint = proof.Pipeline
	}
	localSecret := "requesting-machine-only-secret"
	if proof != nil && proof.Secret != "" {
		localSecret = proof.Secret
	}
	created, err := member.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "burst-acceptance", LocalSecrets: map[string]string{"TOKEN": localSecret}})
	if err != nil || created.HereBearer == "" {
		t.Fatalf("Cloud-owned intake with local host: %+v %v", created, err)
	}
	here := newClient(created.HereBearer, false)
	available := float64(64)
	if saturated {
		available = 0
	}
	claimed, err := here.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{capability.UnitTrigger}, RunID: created.RunID, Capacity: &match.Resources{Cores: 64}, AvailableCores: available})
	if err != nil || claimed.Claim == nil {
		t.Fatalf("local owner claim: %+v %v", claimed, err)
	}
	if !claimed.Claim.OwnerMachine {
		t.Fatal("the requester's here award is not marked as the owner's machine")
	}
	owner := newClient(claimed.Claim.Capability, false)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	if proof != nil {
		plan = proof.Plan
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: created.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	resolution := runspec.Resolved{Entrypoint: input.Source.Entrypoint, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: resolved, Guards: runspec.Guards{Passed: true}}
	if proof != nil {
		resolution.Engine = proof.Engine
	}
	accepted, err := owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: resolution})
	if err != nil || accepted.Spec.Hash == "" || accepted.Spec.Request.Request.Source.BundleDigest.Value != digest {
		t.Fatalf("sealed burst plan: %+v %v", accepted, err)
	}
	if _, err := owner.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeReady), Cause: string(store.CauseRelease)}); err != nil {
		t.Fatal(err)
	}
	laterClaimants := []controllerwire.Controller{cloud}
	if !saturated {
		laterClaimants = append(laterClaimants, worker)
	}
	for _, later := range laterClaimants {
		if result, err := later.Claim(t.Context(), poll); err != nil || result.Claim != nil {
			t.Fatalf("here preference did not hold later placement: %+v %v", result, err)
		}
	}
	var awarded controllerwire.ClaimResponse
	if placement == "here" || placement == "here-reserved" {
		request := controllerwire.ReconcileRequest{Policy: "none"}
		if placement == "here-reserved" {
			if _, err := owner.Renew(t.Context(), claimed.Claim.UnitID, controllerwire.RenewalRequest{Available: &match.Resources{}}); err != nil {
				t.Fatal(err)
			}
			miss, err := owner.ReconcileJob(t.Context(), created.RunID, "job", request)
			if err != nil || miss.Kind != string(store.Dispatch) || miss.Claim != nil {
				t.Fatalf("zero capacity claimed without reservation: %+v %v", miss, err)
			}
			request.Reserved = &match.Resources{Cores: 1}
		}
		result, err := owner.ReconcileJob(t.Context(), created.RunID, "job", request)
		if err != nil || result.Claim == nil || result.Claim.Runner.Name != claimed.Claim.Runner.Name {
			t.Fatalf("local owner did not execute before overflow: %+v %v", result, err)
		}
		awarded.Claim = result.Claim
	} else {
		now = now.Add(2 * time.Second)
		if result, err := cloud.Claim(t.Context(), poll); err != nil || result.Claim != nil {
			t.Fatalf("personal-machine preference did not hold Cloud: %+v %v", result, err)
		}
		selected, wantRunner := worker, "burst-desktop"
		if cloudWorker {
			now = now.Add(2 * time.Second)
			engineClaimHTTP(t, s.Handler(), memberBearer, "PUT", "/api/v1/overflow", `{"monthly_minutes":0}`, http.StatusOK)
			if denied, err := cloud.Claim(t.Context(), poll); err != nil || denied.Claim != nil {
				t.Fatalf("zero user cap admitted Cloud after holds elapsed: %+v %v", denied, err)
			}
			engineClaimHTTP(t, s.Handler(), memberBearer, "PUT", "/api/v1/overflow", `{"monthly_minutes":null}`, http.StatusOK)
			selected, wantRunner = cloud, "burst-launcher"
		} else {
			engineClaimHTTP(t, s.Handler(), memberBearer, "PUT", "/api/v1/overflow", `{"monthly_minutes":0}`, http.StatusOK)
		}
		if placement == "cloud-retry" {
			engineSQL(t, s.store, `UPDATE nodes SET attempt=2 WHERE team='a' AND run_id=? AND node_id='job'`, created.RunID)
		}
		if cloudWorker && proof != nil && proof.ClaimCloud != nil {
			launcherOptions := options
			launcherOptions.Launcher = true
			launcherOptions.Bearer = func(ctx context.Context) (string, error) { return cloudBearer, nil }
			unit := proof.ClaimCloud(launcherOptions)
			awarded.Claim = &unit
		} else {
			awarded, err = selected.Claim(t.Context(), poll)
		}
		if err != nil || awarded.Claim == nil || awarded.Claim.Runner.Name != wantRunner {
			t.Fatalf("worker after placement hold: %+v %v", awarded, err)
		}
	}
	spec := source.Spec{RepoKey: string(input.Git.Repository.Key), RemoteURL: input.Source.Location.RemoteURL, SHA: sha, PipelineDir: ".sparkwing", BundleDigest: digest}
	if cloudWorker {
		rawBootstrap := awarded.Claim.Capability
		engineClaimHTTP(t, s.Handler(), rawBootstrap, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, "", http.StatusForbidden)
		engineClaimHTTP(t, s.Handler(), rawBootstrap, "POST", "/api/v1/data/upload", `{"digest":"`+digest+`","size":1}`, http.StatusForbidden)
		engineClaimHTTP(t, s.Handler(), rawBootstrap, "POST", "/api/v1/data/has-many", `{"digests":["`+digest+`"]}`, http.StatusForbidden)
		bootstrapOptions := options
		bootstrapOptions.Bearer = func(context.Context) (string, error) { return awarded.Claim.Capability, nil }
		burstAcceptanceCheckout(t, bootstrapOptions, created.RunID, spec)
		bootstrap := newClient(awarded.Claim.Capability, false)
		if proof != nil && proof.ExchangeCloud != nil {
			unit := proof.ExchangeCloud(bootstrapOptions, *awarded.Claim)
			awarded.Claim = &unit
		} else {
			exchanged, err := bootstrap.Exchange(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			awarded.Claim = &exchanged.Claim
		}
		engineClaimHTTP(t, s.Handler(), rawBootstrap, "POST", "/api/v1/data/download", `{"digest":"`+digest+`"}`, http.StatusUnauthorized)
		if _, err := bootstrap.Exchange(t.Context()); err == nil {
			t.Fatal("bootstrap exchanged more than once")
		}
	}
	if result, err := cloud.Claim(t.Context(), poll); err != nil || result.Claim != nil {
		t.Fatalf("another claimant acquired the same job: %+v %v", result, err)
	}
	workerOptions := options
	workerOptions.Bearer = func(context.Context) (string, error) { return awarded.Claim.Capability, nil }
	secretReply := sourceUploadHTTP(s.Handler(), awarded.Claim.Capability, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, "")
	var secret controllerwire.SecretValue
	if secretReply.Code != http.StatusOK || json.Unmarshal(secretReply.Body.Bytes(), &secret) != nil || secret.Value != localSecret || !secret.Masked || secretReply.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("declared local secret release: HTTP %d", secretReply.Code)
	}
	if response := sourceUploadHTTP(s.Handler(), awarded.Claim.Capability, "GET", "/api/v1/secrets/UNDECLARED?run="+created.RunID, ""); response.Code == http.StatusOK {
		t.Fatal("claiming node read undeclared local secret")
	}
	burstAcceptanceCheckout(t, workerOptions, created.RunID, spec)
	foreignBundle := filepath.Join(t.TempDir(), "foreign.bundle")
	if err := os.WriteFile(foreignBundle, []byte("another run's private source"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := input
	foreign.LocalSecretNames = nil
	foreign.Source.BundleDigest = runspec.Optional[string]{}
	foreignDigest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: foreign.Git, Source: foreign.Source, Path: foreignBundle, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	foreign.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: foreignDigest}
	if _, err := member.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: foreign, IdempotencyKey: "foreign-source"}); err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, s.Handler(), awarded.Claim.Capability, "POST", "/api/v1/data/download", `{"digest":"`+foreignDigest+`"}`, http.StatusForbidden)
	for _, write := range []struct{ path, body string }{
		{"/api/v1/data/upload", `{"digest":"` + digest + `","size":1}`},
		{"/api/v1/data/records", `{"kind":"output","name":"result","digest":"` + digest + `"}`},
	} {
		response := sourceUploadHTTP(s.Handler(), awarded.Claim.Capability, "POST", write.path, write.body)
		if response.Code >= http.StatusOK && response.Code < http.StatusMultipleChoices {
			t.Fatalf("pre-attempt source read granted output write at %s", write.path)
		}
	}
	if placement == "cloud-retry" {
		previousAttempt := int64(2)
		workerClient := newClient(awarded.Claim.Capability, false)
		started, err := workerClient.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeRunning), Cause: string(store.CauseStart), SourceAttempt: &previousAttempt})
		if err != nil || started.Attempt != 3 {
			t.Fatalf("retry start: %+v %v", started, err)
		}
		replayed, err := workerClient.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeRunning), Cause: string(store.CauseStart), SourceAttempt: &previousAttempt})
		if err != nil || !replayed.Replayed || replayed.Attempt != 3 || replayed.StateSeq != started.StateSeq {
			t.Fatalf("retry start replay: %+v %v", replayed, err)
		}
		forged := int64(1)
		if _, err := workerClient.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeRunning), Cause: string(store.CauseStart), SourceAttempt: &forged}); err == nil {
			t.Fatal("forged transition source ordinal accepted")
		}
		burstAcceptanceCheckout(t, workerOptions, created.RunID, spec)
	}
	snapshot, err := owner.ReadSnapshot(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal([]any{snapshot, accepted})
	if err != nil || strings.Contains(string(persisted), localSecret) {
		t.Fatal("local secret appeared in persisted run or plan")
	}
	if proof != nil {
		proof.Execute(workerOptions, *awarded.Claim, owner)
		completed, err := owner.ReadSnapshot(t.Context(), created.RunID)
		if err != nil || len(completed.Jobs) != 1 || completed.Jobs[0].Outcome != string(store.NodeSuccess) {
			t.Fatalf("owner did not observe successful SDK execution: %+v %v", completed, err)
		}
	}
	if proof == nil && placement != "cloud-retry" {
		workerClient := newClient(awarded.Claim.Capability, false)
		sourceAttempt := int64(0)
		if _, err := workerClient.TransitionJob(t.Context(), created.RunID, "job", controllerwire.JobTransitionRequest{To: string(store.NodeRunning), Cause: string(store.CauseStart), SourceAttempt: &sourceAttempt}); err != nil {
			t.Fatalf("worker start before cancellation: %v", err)
		}
	}
	if applied, err := member.Cancel(t.Context(), created.RunID, controllerwire.CancelRequest{Reason: "user_cancelled"}); err != nil || !applied.CancelRequested {
		t.Fatalf("requester cancellation: %+v %v", applied, err)
	}
	units := []*controllerwire.ClaimedUnit{claimed.Claim}
	if proof == nil {
		units = append(units, awarded.Claim)
	}
	for _, unit := range units {
		c := newClient(unit.Capability, false)
		if beat, err := c.Renew(t.Context(), unit.UnitID, controllerwire.RenewalRequest{}); err != nil || !beat.CancelRequested {
			t.Fatalf("cancellation delivery to owner/worker: %+v %v", beat, err)
		}
	}
	if proof != nil && proof.FinishCloud != nil {
		proof.FinishCloud()
	}
}

func burstAcceptanceCheckout(t *testing.T, options client.EngineOptions, run string, spec source.Spec) {
	t.Helper()
	access, err := client.NewSourceAccess(options, run, spec)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.SHA != spec.SHA {
		t.Fatalf("worker source revision=%s, want %s", checkout.SHA, spec.SHA)
	}
	if err := checkout.Close(); err != nil {
		t.Fatal(err)
	}
}

func burstAcceptancePersonalMachine(t *testing.T, s *Server) string {
	t.Helper()
	digest := sha256.Sum256([]byte("private-account-session"))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := s.store.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMachine, Personal: true, Authority: &authority, RegistrationID: "runner:a:burst-desktop", Team: "a", Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "burst-desktop"}, Grants: []capability.Grant{capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"example.test/acme/*"}})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Raw
}
