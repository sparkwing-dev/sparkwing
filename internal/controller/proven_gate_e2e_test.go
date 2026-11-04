package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const provenGateSHA = "0123456789abcdef0123456789abcdef01234567"

type provenGatePass struct {
	Tree     string `json:"tree"`
	Recorded bool   `json:"recorded"`
	Mode     string `json:"mode"`
}

// safety: the SDK reads through the node-host process route, so the read crosses process
// authority, the local host binding check and the controller as a hosted job's does.
type provenLatestServices struct {
	sdkserve.Services
	router http.Handler
	bearer string
}

func (provenLatestServices) Emit(nodeproto.FrameLog) {}

func (s provenLatestServices) Latest(ctx context.Context, pipeline, job string, age time.Duration, fromAnyRun, fromProvenRun bool) (json.RawMessage, error) {
	query := url.Values{"job": {job}, "max_age_ms": {strconv.FormatInt(age.Milliseconds(), 10)}}
	if fromAnyRun {
		query.Set("from_any_run", "true")
	}
	if fromProvenRun {
		query.Set("from_proven_run", "true")
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/node/v1/pipelines/"+pipeline+"/latest?"+query.Encode(), nil)
	request.Header.Set("Authorization", "Bearer "+s.bearer)
	response := httptest.NewRecorder()
	s.router.ServeHTTP(response, request)
	var body struct {
		Error  *apierr.Error   `json:"error"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		return nil, err
	}
	if body.Error != nil {
		return nil, body.Error
	}
	return body.Output, nil
}

// TestProvenGatePassReachesALaterRunAtTheSameCommit runs a producer job
// through claim, attempt start and report on a trusted host, then reads its
// output from a second run at the same commit the way the gate's skip
// predicate does. A refused lookup fails the test.
func TestProvenGatePassReachesALaterRunAtTheSameCommit(t *testing.T) {
	ctx := t.Context()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	var server *controller.Server
	e, _, root := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		server = controller.New(st, nil).WithDataObjects(objects)
		policies := server.StorePolicies()
		policies.ClaimProfiles = childOutputCapacity{}
		return policies, nil
	}))
	handler := server.WithEngineController(e.EngineController).HTTP().API
	httpClient := &http.Client{Transport: childOutputTransport{handler: handler}}

	childOutputSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`,
		[]byte(`{"id":"job","pipeline_refs":[{"pipeline":"gate","job":"record-tree","from_proven_run":true}]}`))
	readerClaim := controller.EngineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"]}`, 200)
	var award struct {
		Claim struct {
			Capability string `json:"capability"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(readerClaim.Body.Bytes(), &award); err != nil || award.Claim.Capability == "" {
		t.Fatalf("reader claim: %v %s", err, readerClaim.Body)
	}

	producer := "gate-a"
	childOutputSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,git_sha,provenance_sha,repo_key,dispatch,intake_mode,created_principal,created_at,started_at,lineage_json,log_retention_class,log_retention_days) SELECT team,?,2,'gate','running',request_json,'gate-a',1,?,?,'gh:1','host','execute','account',1,1,lineage_json,'r30',30 FROM runs WHERE team='a' AND id='run'`,
		producer, provenGateSHA, provenGateSHA)
	childOutputSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels,status,finished_at) VALUES ('a',?,'trigger',1,1,?,?,?,'done',1)`, producer, time.Now().Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	controller.EngineAcceptedFixturePlan(t, e.Store, "a", producer, "record-tree")
	childOutputSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a',?,'record-tree','pending',?,?)`, producer, []byte(`{"id":"record-tree"}`), []byte(`{}`))
	if err := teststore.SeedNodeReady(e.Store, ctx, producer, "record-tree"); err != nil {
		t.Fatal(err)
	}
	claim, err := e.Store.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: "root", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(producer), NodeID: "record-tree"}})
	if err != nil {
		t.Fatal(err)
	}
	producerBearer := string(claim.Bearer)
	path := "/api/v1/runs/" + producer + "/jobs/record-tree/transition"
	controller.EngineAdmitBearer(t, e.Store, producerBearer)
	controller.EngineClaimHTTP(t, handler, producerBearer, "POST", path, `{"to":"running","cause":"start","source_attempt":0}`, 200)
	producerHost, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: httpClient, Bearer: func(context.Context) (string, error) { return producerBearer, nil }}, producer, "record-tree")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producerHost.Close() })
	producerPrincipal, err := e.Store.AsOperator().LookupCapability(ctx, sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := producerHost.BindStorage(ctx, producerPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := json.Marshal(provenGatePass{Tree: "tree-1", Recorded: true, Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := (blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: storage.Authority.Upload.Value, Pipeline: "gate"}).Put(ctx, pass)
	if err != nil {
		t.Fatal(err)
	}
	report, err := json.Marshal(map[string]any{"to": "success", "cause": "report", "source_attempt": 1, "result": map[string]any{"outcome": "success", "output": record.Key}})
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, producerBearer, "POST", path, string(report), 200)
	childOutputSQL(t, e.Store, `UPDATE runs SET status='success',finished_at=? WHERE team='a' AND id=?`, time.Now().UnixNano(), producer)
	// safety: both runs are sealed as App-proven pushes of one commit, and the producer's host marked trusted,
	// after their claims, since trustedHostRecordedAtClaim covers the claim rules and the subject here is the read path.
	childOutputSQL(t, e.Store, `UPDATE nodes SET trusted_host=1 WHERE team='a' AND run_id=?`, producer)
	feature := sealAppProof(t, "refs/heads/feature")
	reseal(t, e.Store, producer, feature, nil)
	reseal(t, e.Store, "run", sealAppProof(t, "refs/heads/main"), nil)

	raw := award.Claim.Capability
	controller.EngineAdmitBearer(t, e.Store, raw)
	controller.EngineClaimHTTP(t, handler, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: httpClient, Bearer: func(context.Context) (string, error) { return raw, nil }}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	principal, err := host.Principal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.ProcessClient(ctx, principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	authorities, err := nodehost.NewProcessAuthorities(nodehost.ProcessAuthorityDependencies{Parent: host, Bindings: host})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authorities.Close() })
	router := routepolicy.New(routepolicy.Authorization{Authenticator: capability.NewAuthenticator(authorities), Bindings: authorities})
	if err := nodehost.RegisterProcessServices(router, authorities.Resolve); err != nil {
		t.Fatal(err)
	}
	processes := nodehost.NativeProcesses{Authorities: authorities, Masks: nodehost.NewMaskRegistry(secrets.NewMasker()), URL: "http://127.0.0.1:1"}
	binding := principal.Capability.Binding
	binding.Attempt = 1
	data := principal.Capability.Data
	if data != nil {
		copied := *data
		copied.Attempt = 1
		data = &copied
	}
	grant, err := processes.Delegate(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: principal.Capability.Digest}, capability.DelegateSpec{Parent: principal.Authority, Audience: capability.NodeHost, Binding: binding, Grants: principal.Capability.Grants, Repositories: principal.Capability.Repositories, Data: data, ExpiresAt: principal.Capability.ExpiresAt}, nodehost.ProcessServices{
		RunID: "run", Job: nodeproto.JobSpec{ID: "job", References: []nodeproto.PipelineReference{{Pipeline: "gate", Job: "record-tree", FromProvenRun: true}}},
		Latest: func(ctx context.Context, auth capability.Authorized, pipeline, job string, age time.Duration, fromAnyRun bool) (nodehost.ProcessOutput, error) {
			proven := auth.Target.Reference != nil && auth.Target.Reference.FromProvenRun
			run, output, err := process.Latest(ctx, pipeline, job, age, fromAnyRun, proven)
			return nodehost.ProcessOutput{RunID: run, Output: output}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = grant.Revoke() })
	sdk := sdkserve.DefaultOperations.Context(ctx, nodeproto.RunContext{RunID: "run"}, nodeproto.RunnerIdentity{}, provenLatestServices{router: router, bearer: grant.Binding.Routes.Bearer})
	ref := sparkwing.RefToLastRun[provenGatePass]("gate", "record-tree", sparkwing.FromProvenRunAtCommit())
	read := func() (pass provenGatePass, found bool) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("the proven lookup failed instead of answering: %v", recovered)
			}
		}()
		return ref.TryGet(sdk)
	}
	if got, found := read(); !found || got != (provenGatePass{Tree: "tree-1", Recorded: true, Mode: "full"}) {
		t.Fatalf("the later run read %+v, found=%v; want the producer's full pass", got, found)
	}

	for name, mutate := range map[string]struct{ break_, restore func() }{
		"an untrusted producer host": {
			break_: func() {
				childOutputSQL(t, e.Store, `UPDATE nodes SET trusted_host=0 WHERE team='a' AND run_id=?`, producer)
			},
			restore: func() {
				childOutputSQL(t, e.Store, `UPDATE nodes SET trusted_host=1 WHERE team='a' AND run_id=?`, producer)
			},
		},
		"a producer with a sealed environment": {
			break_:  func() { reseal(t, e.Store, producer, feature, []string{"GOFLAGS"}) },
			restore: func() { reseal(t, e.Store, producer, feature, nil) },
		},
		"a producer narrowed by --only": {
			break_: func() {
				reseal(t, e.Store, producer, feature, nil, func(o *runspec.Options) { o.Only.Value = "record-tree" })
			},
			restore: func() { reseal(t, e.Store, producer, feature, nil, func(o *runspec.Options) { o.Only.Value = "" }) },
		},
		"a producer at another commit": {
			break_: func() {
				childOutputSQL(t, e.Store, `UPDATE runs SET provenance_sha=? WHERE team='a' AND id=?`, "fedcba9876543210fedcba9876543210fedcba98", producer)
			},
			restore: func() {
				childOutputSQL(t, e.Store, `UPDATE runs SET provenance_sha=? WHERE team='a' AND id=?`, provenGateSHA, producer)
			},
		},
	} {
		mutate.break_()
		if got, found := read(); found {
			t.Errorf("%s: the later run read %+v", name, got)
		}
		mutate.restore()
	}
	if _, found := read(); !found {
		t.Fatal("restoring the producer did not restore the pass")
	}
}

func sealAppProof(t *testing.T, ref string) runspec.Provenance {
	t.Helper()
	p, err := runspec.AppProvenance(runspec.AppFacts{Repository: "acme/repo", Event: "push", RepositoryID: 1, DeliveryKey: "delivery-" + ref, VerifiedAt: time.Unix(1700000000, 0), Ref: ref, SHA: provenGateSHA, DefaultBranch: "main", DefaultBranchPush: ref == "refs/heads/main"}, runspec.Optional[runspec.PullRequest]{}, "gate")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reseal(t *testing.T, st *store.Store, run string, p runspec.Provenance, env []string, options ...func(*runspec.Options)) {
	t.Helper()
	var body []byte
	var pipeline string
	if err := teststore.SQL(t, st).QueryRowContext(t.Context(), `SELECT request_json,pipeline FROM runs WHERE team='a' AND id=?`, run).Scan(&body, &pipeline); err != nil {
		t.Fatal(err)
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(body, &sealed); err != nil {
		t.Fatal(err)
	}
	sealed.Request.Pipeline, sealed.Request.Git.SHA, sealed.Request.Git.Repository.Key, sealed.Request.Env.Names = pipeline, provenGateSHA, "gh:1", env
	for _, set := range options {
		set(&sealed.Request.Options)
	}
	sealed.ProofEligible = sealed.Request.ProofEligible()
	childOutputSQL(t, st, `UPDATE runs SET request_json=?,git_sha=?,repo_key='gh:1' WHERE team='a' AND id=?`, controller.SealWith(t, sealed, p), provenGateSHA, run)
}
