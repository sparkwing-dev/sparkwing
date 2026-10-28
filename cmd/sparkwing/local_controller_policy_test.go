package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type policyListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *policyListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *policyListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*policyListener) Addr() net.Addr { return &net.UnixAddr{Name: "api.sock", Net: "unix"} }

type policyConnection struct {
	net.Conn
	peer localhostwire.LocalPeerIdentity
}

func (c policyConnection) PeerIdentity() localhostwire.LocalPeerIdentity { return c.peer }

func productionLocalPolicyClient(t *testing.T) client.EngineOptions {
	t.Helper()
	return productionLocalPolicyClientAt(t, t.TempDir())
}

func productionLocalPolicyClientAt(t *testing.T, home string) client.EngineOptions {
	t.Helper()
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	owner, err := openLocalController(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := owner.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	peer := localhostwire.LocalPeerIdentity{GOOS: runtime.GOOS, UID: &uid}
	if runtime.GOOS == "windows" {
		subject, err := wingd.CurrentLocalSubject()
		if err != nil {
			t.Fatal(err)
		}
		peer = localhostwire.LocalPeerIdentity{GOOS: runtime.GOOS, SID: strings.TrimPrefix(subject, "local:")}
	}
	listener := &policyListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- wingd.ServeAPI(ctx, listener, owner.HTTP()) }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case listener.connections <- policyConnection{Conn: server, peer: peer}:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: transport}}
}

func TestProductionLocalControllerAdmitsRun(t *testing.T) {
	for _, version := range []string{"(devel)", "v1.2.3"} {
		t.Run(version, func(t *testing.T) {
			previous := Version
			Version = version
			t.Cleanup(func() { Version = previous })
			for _, format := range []string{"unborn", "sha1", "sha256"} {
				t.Run(format, func(t *testing.T) { testProductionLocalControllerAdmitsRun(t, format) })
			}
		})
	}
}

func testProductionLocalControllerAdmitsRun(t *testing.T, format string) {
	previous := http.DefaultClient
	requests := 0
	http.DefaultClient = &http.Client{Transport: queueHostTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previous })
	options := productionLocalPolicyClient(t)
	engine, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	objectFormat := format
	if format == "unborn" {
		objectFormat = "sha1"
	}
	runSnapshotGit(t, checkout, "init", "--quiet", "--object-format="+objectFormat)
	runSnapshotGit(t, checkout, "config", "user.name", "Local Test")
	runSnapshotGit(t, checkout, "config", "user.email", "local@example.test")
	if format != "unborn" {
		runSnapshotGit(t, checkout, "commit", "--quiet", "--allow-empty", "-m", "source")
	}
	if err := os.MkdirAll(filepath.Join(checkout, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:example.com/acme/repo", LocalCheckout: checkout, PipelineDir: ".sparkwing"}, source.Options{Base: procenv.Base{Local: true, Captured: os.Environ()}})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"unborn": 0, "sha1": 40, "sha256": 64}[format]; len(entry.SHA) != want {
		t.Fatalf("acquired SHA length=%d, want %d", len(entry.SHA), want)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	request := controllerwire.CreateRunRequest{IdempotencyKey: "local-policy", Request: runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "example",
		Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
		Options: runspec.Options{Priority: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}},
		Source:  runspec.Source{Ref: entry.SHA, Location: runspec.SourceLocation{LocalCheckout: checkout, PipelineDir: ".sparkwing"}},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}, SHA: entry.SHA},
	}}
	admitted, err := engine.CreateRun(t.Context(), request)
	if format == "unborn" {
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs || admitted.RunID != "" {
			t.Fatalf("unresolved local source admitted: %+v: %v", admitted, err)
		}
		reader, err := client.NewRunReader(options)
		if err != nil {
			t.Fatal(err)
		}
		page, err := reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20})
		if err != nil || len(page.Runs) != 0 {
			t.Fatalf("refused source persisted a run: %+v: %v", page, err)
		}
		return
	}
	if err != nil {
		t.Fatal("real local controller admission:", err)
	}
	replay, err := engine.CreateRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != admitted.RunID {
		t.Fatalf("replay: %+v: %v", replay, err)
	}
	exerciseLocalRun(t, options, admitted, request.Request)
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Status != string(runstate.RunSuccess) {
		t.Fatalf("development run page: %+v: %v", page, err)
	}
	if Version == "v1.2.3" && (len(page.Runs[0].Notices) != 1 || page.Runs[0].Notices[0].Kind != engineid.NoticePolicyUnavailable) {
		t.Fatalf("offline policy warning missing from local status: %+v", page.Runs[0].Notices)
	}
	compatibility, err := engine.Compatibility(t.Context(), "v1.2.3")
	if err != nil || compatibility.EngineRelease != "v1.2.3" {
		t.Fatalf("offline local compatibility: %+v: %v", compatibility, err)
	}
	if requests != 0 {
		t.Fatalf("local controller fetched policy %d times", requests)
	}
}

func exerciseLocalRun(t *testing.T, options client.EngineOptions, admitted controllerwire.CreateRunResponse, expected ...runspec.Request) {
	t.Helper()
	ctx := t.Context()
	engine, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	award, err := engine.Claim(ctx, controllerwire.ClaimRequest{RunID: admitted.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: "v1.2.3"})
	if err != nil || award.Claim == nil {
		t.Fatalf("owner claim: %+v: %v", award, err)
	}
	if len(expected) > 0 {
		sealed := award.Claim.Request
		request := expected[0]
		if !sealed.Request.Options.OwnerExecutionTimeout.Set || sealed.Request.Options.OwnerExecutionTimeout.Value != 0 || sealed.Request.Options.Priority != request.Options.Priority || sealed.Request.Source != request.Source || sealed.Request.Git.SHA != request.Git.SHA || sealed.AuthorityRef.ID == "" {
			t.Fatal("local claim lost its accepted inputs")
		}
	}
	options.Bearer = func(context.Context) (string, error) { return award.Claim.Capability, nil }
	ownerEngine, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	host, err := client.NewLocalHostClient(options, admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Principal(ctx); err != nil {
		t.Fatal("owner refresh:", err)
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Inline: true, Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: award.Claim.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal("plan settings:", err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ownerEngine.AcceptPlan(ctx, admitted.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: engineid.Identity{Version: "v1.2.3", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}}, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil || accepted.Spec.Hash == "" {
		t.Fatalf("plan acceptance: %v", err)
	}
	if _, err := host.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(admitted.RunID), To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
		t.Fatal("run start:", err)
	}
	claim, err := ownerEngine.ReconcileJob(ctx, admitted.RunID, "build", controllerwire.ReconcileRequest{Policy: string(runstate.SourceNone)})
	if err != nil || claim.Claim == nil {
		t.Fatalf("node claim: %+v: %v", claim, err)
	}
	nodeOptions := options
	nodeOptions.Bearer = func(context.Context) (string, error) { return claim.Claim.Capability, nil }
	node, err := client.NewNodeHostClient(nodeOptions, admitted.RunID, "build")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Error(err)
		}
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "build"}
	if decision, err := node.MayStart(ctx, runstate.CapacityBinding{RunID: admitted.RunID, NodeID: "build", Generation: claim.Claim.Binding.Generation}, runstate.Actor{}); err != nil || decision.Kind != runstate.AcquireGranted {
		t.Fatalf("node admission: %+v: %v", decision, err)
	}
	started, err := node.TransitionNode(ctx, ref, runstate.Actor{}, runstate.NodeCommandPayload{To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: new(runspec.AttemptOrdinal)})
	if err != nil {
		t.Fatal("node start:", err)
	}
	principal, err := node.Principal(ctx)
	if err != nil {
		t.Fatal("node refresh:", err)
	}
	storage, err := node.BindStorage(ctx, principal)
	if err != nil {
		t.Fatal("storage:", err)
	}
	output := blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: storage.Authority.Upload.Value, Pipeline: "example", Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}
	record, err := output.Put(ctx, []byte(`{"value":"stored"}`))
	if err != nil {
		t.Fatal("output commit:", err)
	}
	data, err := output.Read(ctx, record.Key)
	if err != nil || string(data) != `{"value":"stored"}` {
		t.Fatalf("output read: %s: %v", data, err)
	}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: principal.Capability.Team, RunID: admitted.RunID, Attempt: &runspec.AttemptIdentity{Team: principal.Capability.Team, RunID: admitted.RunID, NodeID: "build", Generation: principal.Capability.Binding.Generation, Attempt: principal.Capability.Binding.Attempt}}
	uploader, err := node.LogUploader(ctx, principal, subject)
	if err != nil {
		t.Fatal("logs:", err)
	}
	sink, err := logseg.NewSink(ctx, uploader, logseg.Options{Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Append([]byte("production local log\n"), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Close(ctx, logseg.End{}); err != nil {
		t.Fatal("log commit:", err)
	}
	request, err := http.NewRequestWithContext(ctx, "GET", options.URL+"/api/v1/runs/"+admitted.RunID+"/jobs/build/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := options.HTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(data), "production local log") {
		t.Fatalf("log read: HTTP %d %s: %v %v", response.StatusCode, data, err, closeErr)
	}
	if _, err := node.TransitionNode(ctx, ref, runstate.Actor{}, runstate.NodeCommandPayload{To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: new(started.Attempt), Extra: runstate.NodeCommandExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}}); err != nil {
		t.Fatal("node finish:", err)
	}
	if _, err := host.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: runspec.RunID(admitted.RunID), Cause: runstate.RunFinish}}); err != nil {
		t.Fatal("run finish:", err)
	}
	detail, err := engine.ReadRun(ctx, admitted.RunID)
	if err != nil || detail.Status != string(runstate.RunSuccess) || detail.FinishedAt == nil || len(detail.Jobs) != 1 || detail.Jobs[0].Outcome != string(runstate.NodeSuccess) {
		t.Fatalf("completed run: %+v: %v", detail, err)
	}
	if _, err := host.Principal(ctx); err == nil {
		t.Fatal("completed owner retained live execution authority")
	}
}
