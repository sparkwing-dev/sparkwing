package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type childOutputTransport struct {
	handler http.Handler
	after   func(*http.Request, *httptest.ResponseRecorder)
}

func (c childOutputTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	c.handler.ServeHTTP(w, r)
	if c.after != nil {
		c.after(r, w)
	}
	return w.Result(), nil
}

type childAwaitServices struct {
	sdkserve.Services
	process nodehost.ProcessServices
}

type childWeather struct {
	Weather string `json:"weather"`
	Degrees int    `json:"degrees"`
}

type childWeatherPipeline struct{}

type childWeatherStamp struct {
	sparkwing.Produces[childWeather]
}

func (childWeatherStamp) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", func(context.Context) (childWeather, error) {
		return childWeather{Weather: "clear", Degrees: 72}, nil
	}), nil
}

func (childWeatherPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "stamp", childWeatherStamp{})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("child-output-weather-fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return childWeatherPipeline{} })
}

func (s childAwaitServices) RunPipeline(ctx context.Context, request sdkserve.ChildRequest) (json.RawMessage, error) {
	run, err := s.process.StartChild(ctx, capability.Authorized{}, nodehost.ChildRequest{Pipeline: request.Pipeline, Args: request.Args})
	if err != nil {
		return nil, err
	}
	result, err := s.process.AwaitChild(ctx, capability.Authorized{}, run, request.Job, time.Minute)
	return result.Output, err
}

func TestRunAndAwaitChildOutputThroughLocalController(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	e, _, root := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	if _, err := e.Store.DB().ExecContext(ctx, `UPDATE teams SET next_run_number=2 WHERE name='a'`); err != nil {
		t.Fatal(err)
	}
	objectDirectory := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: objectDirectory, Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	handler := controller.New(e.Store, nil).WithEngineController(e).WithDataObjects(objects).HTTP().API
	parent := controller.EngineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"available_cores":1}`, 200)
	var award struct {
		Claim struct {
			Capability string `json:"capability"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(parent.Body.Bytes(), &award); err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, award.Claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	transport := &childOutputTransport{handler: handler}
	httpClient := &http.Client{Transport: transport}
	options := client.EngineOptions{URL: "http://local", HTTP: httpClient, Bearer: func(context.Context) (string, error) { return award.Claim.Capability, nil }}
	host, err := client.NewNodeHostClient(options, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	remote, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := host.Principal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err := nodehost.NewRemoteHostStore(remote, host, principal.Capability.UnitID, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	cfg := localcontroller.WithClient(localcontroller.NativeConfig{Store: state}, host)
	services, err := cfg.Services(ctx, principal, nodeproto.JobSpec{ID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	transport.after = func(r *http.Request, w *httptest.ResponseRecorder) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/children") || w.Code != http.StatusCreated {
			return
		}
		var child struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &child); err != nil {
			t.Fatal(err)
		}
		childID = child.RunID
		finishChildOutput(t, e.Store, handler, httpClient, child.RunID)
		childOutputSQL(t, e.Store, `UPDATE runs SET status='success',finished_at=? WHERE team='a' AND id=?`, time.Now().UnixNano(), child.RunID)
	}
	sdkContext := sdkserve.DefaultOperations.Context(ctx, nodeproto.RunContext{RunID: "run"}, nodeproto.RunnerIdentity{}, childAwaitServices{process: services})
	got, err := sparkwing.RunAndAwait[childWeather, sparkwing.NoInputs](sdkContext, "weather-report", "stamp")
	if err != nil || got != (childWeather{Weather: "clear", Degrees: 72}) {
		t.Fatalf("typed child output: %+v: %v", got, err)
	}
	tenant, err := e.Store.ForTeam(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: "run", ChildRunID: childID, NodeID: "stamp"}}
	metadata, err := tenant.ReadChildOutput(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("current creating job only", func(t *testing.T) {
		property := func(input []byte, dimension uint8) bool {
			changed := a
			noise := fmt.Sprintf("foreign-%x", sha256.Sum256(input))
			switch dimension % 8 {
			case 0:
				changed.Target.Team = noise
			case 1:
				changed.Target.RunID = noise
			case 2:
				changed.Target.ChildRunID = noise
			case 3:
				changed.Target.NodeID = noise
			case 4:
				childOutputSQL(t, e.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
				defer childOutputSQL(t, e.Store, `UPDATE nodes SET generation=generation-1 WHERE team='a' AND run_id='run' AND node_id='job'`)
			case 5:
				childOutputSQL(t, e.Store, `UPDATE nodes SET claimed_by=? WHERE team='a' AND run_id='run' AND node_id='job'`, noise)
				defer childOutputSQL(t, e.Store, `UPDATE nodes SET claimed_by=? WHERE team='a' AND run_id='run' AND node_id='job'`, principal.Capability.Binding.Holder)
			case 6:
				childOutputSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
				defer childOutputSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=NULL WHERE team='a' AND id='run'`)
			case 7:
				childOutputSQL(t, e.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, principal.Capability.Prefix)
				defer childOutputSQL(t, e.Store, `UPDATE capabilities SET revoked_at=NULL WHERE prefix=?`, principal.Capability.Prefix)
			}
			object, err := tenant.ReadChildOutput(ctx, changed)
			return err != nil && object == (blob.Blob{})
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
			t.Fatal(err)
		}
	})
	path := "/api/v1/runs/run/children/" + childID + "/jobs/stamp/output"
	controller.EngineClaimHTTP(t, handler, award.Claim.Capability, "GET", path, "", 200)
	controller.EngineClaimHTTP(t, handler, root, "GET", path, "", 403)
	key, err := blob.ObjectKey(metadata.Team, metadata.Digest)
	if err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(objectDirectory, filepath.FromSlash(key))
	if err := os.WriteFile(objectPath, bytes.Repeat([]byte{'x'}, int(metadata.Size)), 0o600); err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, award.Claim.Capability, "GET", path, "", 422)
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, award.Claim.Capability, "GET", path, "", 404)
}

func childOutputSQL(t *testing.T, st *store.Store, query string, args ...any) {
	t.Helper()
	if _, err := st.DB().ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

type childOutputCapacity struct{}

func (childOutputCapacity) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	return match.Profile{Capacity: match.Resources{Cores: 2}}, match.Resources{Cores: 2}, nil
}

func finishChildOutput(t *testing.T, st *store.Store, handler http.Handler, httpClient *http.Client, run string) {
	t.Helper()
	st.SetClaimProfileResolver(childOutputCapacity{})
	if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a',?,'stamp','pending',1,?,?)`, run, []byte(`{"id":"stamp"}`), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "root", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: run, NodeID: "stamp"}, Capacity: match.Resources{Cores: 2}, Available: match.Resources{Cores: 2}})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(claim.Bearer)
	path := "/api/v1/runs/" + run + "/jobs/stamp/transition"
	controller.EngineClaimHTTP(t, handler, raw, "POST", path, `{"to":"running","cause":"start","source_attempt":0}`, 200)
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: httpClient, Bearer: func(context.Context) (string, error) { return raw, nil }}, run, "stamp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := st.LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := host.BindStorage(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	ops := sdkserve.DefaultOperations
	built, _, err := ops.Plan(t.Context(), "child-output-weather-fixture", map[string]string{}, nodeproto.RunContext{RunID: run, Pipeline: "weather-report"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ops.RunStep(ops.Context(t.Context(), nodeproto.RunContext{RunID: run}, nodeproto.RunnerIdentity{}, nil), built, nodeproto.Request{Job: "stamp", Step: "run", Attempt: 1, Settled: []string{}})
	if err != nil || result.Outcome != "success" {
		t.Fatalf("child stamp: %+v: %v", result, err)
	}
	tenant, err := st.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	record, err := (blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: storage.Authority.Upload.Value, Pipeline: snapshot.Run.Request.Request.Pipeline}).Put(t.Context(), result.Output)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"to": "success", "cause": "report", "source_attempt": 1, "result": map[string]any{"outcome": "success", "output": record.Key}})
	if err != nil {
		t.Fatal(err)
	}
	controller.EngineClaimHTTP(t, handler, raw, "POST", path, string(body), 200)
}

func TestJobOutputThroughNativeClientAndCurrentReadSet(t *testing.T) {
	e, _, root := controller.EngineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim, capability.TriggersClaim})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	handler := controller.New(e.Store, nil).WithDeployment(deploymenttest.Team(t)).WithSecretsCipher(cipher).WithEngineController(e).WithDataObjects(objects).HTTP().API
	claim := controller.EngineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"],"available_cores":1}`, 200)
	var award struct {
		Claim struct {
			Capability string `json:"capability"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &award); err != nil {
		t.Fatal(err)
	}
	raw := award.Claim.Capability
	controller.EngineClaimHTTP(t, handler, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	httpClient := &http.Client{Transport: childOutputTransport{handler: handler}}
	finishChildOutput(t, e.Store, handler, httpClient, "run")
	childOutputSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","deps":["stamp"]}`))
	options := client.EngineOptions{URL: "http://local", HTTP: httpClient, Bearer: func(context.Context) (string, error) { return raw, nil }}
	host, err := client.NewNodeHostClient(options, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.Output(t.Context(), "run", "stamp")
	if err != nil || string(output) != `{"weather":"clear","degrees":72}` {
		t.Fatalf("output = %s: %v", output, err)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: "run", NodeID: "stamp"}}
	property := func(noise []byte, dimension uint8) bool {
		changed := a
		foreign := fmt.Sprintf("foreign-%x", sha256.Sum256(noise))
		switch dimension % 7 {
		case 0:
			changed.Target.Team = foreign
		case 1:
			changed.Target.NodeID = foreign
		case 2:
			changed.Target.RunID = foreign
		case 3:
			childOutputSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job"}`))
			defer childOutputSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","deps":["stamp"]}`))
		case 4:
			childOutputSQL(t, e.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, principal.Capability.Prefix)
			defer childOutputSQL(t, e.Store, `UPDATE capabilities SET revoked_at=NULL WHERE prefix=?`, principal.Capability.Prefix)
		case 5:
			childOutputSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id='run'`)
			defer childOutputSQL(t, e.Store, `UPDATE runs SET cancel_requested_at=NULL WHERE team='a' AND id='run'`)
		case 6:
			childOutputSQL(t, e.Store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
			defer childOutputSQL(t, e.Store, `UPDATE nodes SET generation=generation-1 WHERE team='a' AND run_id='run' AND node_id='job'`)
		}
		got, err := tenant.ReadJobOutput(t.Context(), changed)
		return err != nil && got == (blob.Blob{})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	childOutputSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('output-reader','output-reader@example.test',1,1)`)
	childOutputSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','output-reader','reader',1)`)
	session, _, _, err := e.Store.CreateAccountSession(t.Context(), store.Account{ID: "output-reader"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://local/api/v1/runs/run/jobs/stamp/output", nil)
		r.Header.Set("Authorization", "Session "+session)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != 200 {
		t.Fatalf("member read = %d %s", w.Code, w.Body)
	}
	childOutputSQL(t, e.Store, `DELETE FROM memberships WHERE team='a' AND account_id='output-reader'`)
	if w := request(); w.Code != 401 {
		t.Fatalf("removed member read = %d %s", w.Code, w.Body)
	}
	controller.EngineClaimHTTP(t, handler, raw, "GET", "/api/v1/runs/run/jobs/job/output", "", 403)
	controller.EngineClaimHTTP(t, handler, root, "GET", "/api/v1/runs/run/jobs/stamp/output", "", 403)
}
