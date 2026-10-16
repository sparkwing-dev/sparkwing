package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	enginepolicy "github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	peer wingd.LocalPeerIdentity
}

func (c policyConnection) PeerIdentity() wingd.LocalPeerIdentity { return c.peer }

func productionLocalPolicyClient(t *testing.T) client.EngineOptions {
	t.Helper()
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(t.TempDir(), "secrets.key"))
	owner, err := openLocalController(t.Context(), t.TempDir())
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
	peer := wingd.LocalPeerIdentity{GOOS: runtime.GOOS, UID: &uid}
	if runtime.GOOS == "windows" {
		subject, err := wingd.CurrentLocalSubject()
		if err != nil {
			t.Fatal(err)
		}
		peer = wingd.LocalPeerIdentity{GOOS: runtime.GOOS, SID: strings.TrimPrefix(subject, "local:")}
	}
	listener := &policyListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- owner.HTTP().ServeAPI(ctx, listener) }()
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
			testProductionLocalControllerAdmitsRun(t)
		})
	}
}

func testProductionLocalControllerAdmitsRun(t *testing.T) {
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
	request := controllerwire.CreateRunRequest{IdempotencyKey: "local-policy", Request: runspec.Request{
		Version: 1, Team: string(store.DefaultTeam), Pipeline: "example",
		Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
		Options: runspec.Options{Priority: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}},
		Source:  runspec.Source{Location: runspec.SourceLocation{LocalCheckout: t.TempDir(), PipelineDir: ".sparkwing"}},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}, SHA: "head"},
	}}
	admitted, err := engine.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal("real local controller admission:", err)
	}
	if !admitted.Request.Request.Options.OwnerExecutionTimeout.Set || admitted.Request.Request.Options.OwnerExecutionTimeout.Value != 0 {
		t.Fatal("local admission gained a deadline")
	}
	if admitted.Request.Request.Options.Priority != request.Request.Options.Priority || admitted.Request.Request.Source != request.Request.Source {
		t.Fatal("local admission lost explicit zero priority or acquisition location")
	}
	if admitted.RunID == "" || admitted.Request.Request.Pipeline != "example" || admitted.Request.AuthorityRef.ID == "" {
		t.Fatalf("incomplete admission: %+v", admitted)
	}
	replay, err := engine.CreateRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != admitted.RunID {
		t.Fatalf("replay: %+v: %v", replay, err)
	}
	exerciseLocalRun(t, options, admitted)
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 20})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Status != string(store.RunSuccess) {
		t.Fatalf("development run page: %+v: %v", page, err)
	}
	if Version == "v1.2.3" && (len(page.Runs[0].Notices) != 1 || page.Runs[0].Notices[0].Kind != enginepolicy.NoticePolicyUnavailable) {
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

func signedLocalPolicy(t *testing.T) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previousKeys, previousHTTP := releaseauth.TrustedPublicKeys, http.DefaultClient
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(public)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys, http.DefaultClient = previousKeys, previousHTTP })
	now := time.Now().UTC().Add(-time.Minute)
	body, err := json.Marshal(enginepolicy.VersionIndex{Format: 1, PublishedAt: now, Latest: "v1.2.3", Versions: []enginepolicy.VersionInfo{{Version: "v1.2.3", PublishedAt: now, SupportUntil: now.AddDate(1, 0, 0)}}})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, body)
	http.DefaultClient = &http.Client{Transport: queueHostTransport(func(r *http.Request) (*http.Response, error) {
		var data []byte
		switch r.URL.String() {
		case enginepolicy.VersionIndexURL:
			data = body
		case enginepolicy.VersionIndexSignatureURL:
			data = signature
		default:
			return nil, fmt.Errorf("unexpected policy request %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
}

func exerciseLocalRun(t *testing.T, options client.EngineOptions, admitted controllerwire.CreateRunResponse) {
	t.Helper()
	ctx := t.Context()
	engine, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	award, err := engine.Claim(ctx, controllerwire.ClaimRequest{RunID: admitted.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: "v1.2.3", AvailableCores: 1})
	if err != nil || award.Claim == nil {
		t.Fatalf("owner claim: %+v: %v", award, err)
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
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal("plan settings:", err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := ownerEngine.AcceptPlan(ctx, admitted.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Engine: enginepolicy.Identity{Version: "v1.2.3", Pin: enginepolicy.Pin{SDK: enginepolicy.GoSDK, Version: "v1.2.3"}}, Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil || accepted.Spec.Hash == "" {
		t.Fatalf("plan acceptance: %v", err)
	}
	if _, err := host.TransitionRun(ctx, store.RunTransition{RunID: admitted.RunID, To: store.RunRunning, Cause: store.RunStart}); err != nil {
		t.Fatal("run start:", err)
	}
	claim, err := ownerEngine.ReconcileJob(ctx, admitted.RunID, "build", controllerwire.ReconcileRequest{Policy: string(store.SourceNone)})
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
	ref := store.NodeRef{RunID: admitted.RunID, NodeID: "build"}
	started, err := node.TransitionNode(ctx, store.NodeTransition{Ref: ref, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: new(int64)})
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
	if _, err := node.TransitionNode(ctx, store.NodeTransition{Ref: ref, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: &started.Attempt, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSuccess}}}); err != nil {
		t.Fatal("node finish:", err)
	}
	if _, err := host.FinishRunAndUnit(ctx, store.FinishRunRequest{Transition: store.RunTransition{RunID: admitted.RunID, To: store.RunSuccess, Cause: store.RunFinish}}); err != nil {
		t.Fatal("run finish:", err)
	}
	detail, err := engine.ReadRun(ctx, admitted.RunID)
	if err != nil || detail.Status != string(store.RunSuccess) || detail.FinishedAt == nil || len(detail.Jobs) != 1 || detail.Jobs[0].Outcome != string(store.NodeSuccess) {
		t.Fatalf("completed run: %+v: %v", detail, err)
	}
	if _, err := host.Principal(ctx); err == nil {
		t.Fatal("completed owner retained live execution authority")
	}
}
