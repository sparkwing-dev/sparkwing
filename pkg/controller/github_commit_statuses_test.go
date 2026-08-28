package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type recordedGitHubStatus struct {
	Path       string
	Method     string
	Accept     string
	Auth       string
	APIVersion string
	UserAgent  string
	Body       githubCommitStatusRequest
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestGitHubCommitStatusReporterPostsGitHubContract(t *testing.T) {
	received := make(chan recordedGitHubStatus, 1)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- recordedGitHubStatus{
			Path:       r.URL.Path,
			Method:     r.Method,
			Accept:     r.Header.Get("Accept"),
			Auth:       r.Header.Get("Authorization"),
			APIVersion: r.Header.Get("X-GitHub-Api-Version"),
			UserAgent:  r.Header.Get("User-Agent"),
			Body:       body,
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	reporter := newGitHubCommitStatusReporter(
		"github-token",
		"https://sparkwing.example.com/team/",
		github.URL,
		github.Client(),
	)
	shutdownGitHubStatusReporter(t, reporter)
	err := reporter.post(context.Background(), githubCommitStatus{
		Owner:       "acme",
		Repo:        "sample-app",
		SHA:         "1111111111111111111111111111111111111111",
		Pipeline:    "pr-gate",
		RunID:       "run-123",
		State:       "pending",
		Description: "Sparkwing pipeline is running",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	got := <-received
	if got.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.Method)
	}
	wantPath := "/repos/acme/sample-app/statuses/1111111111111111111111111111111111111111"
	if got.Path != wantPath {
		t.Errorf("path = %q, want %q", got.Path, wantPath)
	}
	if got.Accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got.Accept)
	}
	if got.Auth != "Bearer github-token" {
		t.Errorf("Authorization = %q", got.Auth)
	}
	if got.APIVersion != githubStatusAPIVersion {
		t.Errorf("X-GitHub-Api-Version = %q", got.APIVersion)
	}
	if got.UserAgent != "sparkwing-controller" {
		t.Errorf("User-Agent = %q", got.UserAgent)
	}
	if got.Body.State != "pending" || got.Body.Context != "sparkwing/pr-gate" {
		t.Errorf("body = %+v", got.Body)
	}
	if got.Body.TargetURL != "https://sparkwing.example.com/team/runs?run=run-123" {
		t.Errorf("target_url = %q", got.Body.TargetURL)
	}
}

func TestGitHubCommitStatusReporterPreservesRunOrder(t *testing.T) {
	received := make(chan string, 2)
	releasePending := make(chan struct{})
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- body.State
		if body.State == "pending" {
			<-releasePending
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	reporter := newGitHubCommitStatusReporter("token", "", github.URL, github.Client())
	shutdownGitHubStatusReporter(t, reporter)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1",
	}
	pending := base
	pending.State = "pending"
	pending.Description = "running"
	terminal := base
	terminal.State = "success"
	terminal.Description = "passed"

	dispatchAccepted := reporter.reserve(logger, pending)
	reporter.enqueue(logger, terminal)
	select {
	case got := <-received:
		t.Fatalf("status %q arrived before dispatch succeeded", got)
	case <-time.After(50 * time.Millisecond):
	}
	dispatchAccepted(true)
	if got := waitForGitHubState(t, received); got != "pending" {
		t.Fatalf("first state = %q, want pending", got)
	}
	select {
	case got := <-received:
		t.Fatalf("terminal state %q overtook pending", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(releasePending)
	if got := waitForGitHubState(t, received); got != "success" {
		t.Fatalf("second state = %q, want success", got)
	}
}

func TestGitHubCommitStatusRejectedDispatchReleasesRunSlot(t *testing.T) {
	received := make(chan string, 1)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- body.State
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	reporter := newGitHubCommitStatusReporter("token", "", github.URL, github.Client())
	shutdownGitHubStatusReporter(t, reporter)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1",
	}
	pending := base
	pending.State = "pending"
	pending.Description = "running"
	terminal := base
	terminal.State = "success"
	terminal.Description = "passed"

	dispatchAccepted := reporter.reserve(logger, pending)
	reporter.enqueue(logger, terminal)
	select {
	case got := <-received:
		t.Fatalf("status %q arrived before dispatch resolved", got)
	case <-time.After(50 * time.Millisecond):
	}
	dispatchAccepted(false)
	if got := waitForGitHubState(t, received); got != "success" {
		t.Fatalf("state after rejected dispatch = %q, want success", got)
	}
	select {
	case got := <-received:
		t.Fatalf("rejected pending status was posted as %q", got)
	case <-time.After(50 * time.Millisecond):
	}

}

func TestGitHubCommitStatusFromTriggerIsPullRequestOnly(t *testing.T) {
	pr := &store.Trigger{
		ID:            "run-pr",
		Pipeline:      "verify",
		TriggerSource: "github",
		TriggerEnv: map[string]string{
			sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest,
			sparkwing.EnvPRHeadSHA:       "head-sha",
		},
		GithubOwner: "acme",
		GithubRepo:  "sample-app",
	}

	for _, tc := range []struct {
		runStatus   string
		wantState   string
		wantSummary string
	}{
		{runStatus: "pending", wantState: "pending", wantSummary: "Sparkwing pipeline is running"},
		{runStatus: "success", wantState: "success", wantSummary: "Sparkwing pipeline passed"},
		{runStatus: "failed", wantState: "failure", wantSummary: "Sparkwing pipeline failed"},
		{runStatus: "cancelled", wantState: "error", wantSummary: "Sparkwing pipeline could not complete"},
	} {
		t.Run(tc.runStatus, func(t *testing.T) {
			got, ok := githubCommitStatusFromTrigger(pr, tc.runStatus)
			if !ok {
				t.Fatal("pull_request status was skipped")
			}
			if got.State != tc.wantState || got.Description != tc.wantSummary {
				t.Errorf("status = %+v", got)
			}
		})
	}

	push := *pr
	push.TriggerEnv = map[string]string{
		sparkwing.EnvGitHubEventName: "push",
		sparkwing.EnvPRHeadSHA:       "head-sha",
	}
	if _, ok := githubCommitStatusFromTrigger(&push, "success"); ok {
		t.Fatal("push trigger produced a commit status")
	}
	retry := *pr
	retry.TriggerSource = "retry"
	if _, ok := githubCommitStatusFromTrigger(&retry, "success"); ok {
		t.Fatal("retry trigger produced a commit status")
	}
}

func TestGitHubCommitStatusReporterDropsWhenQueueIsFull(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	received := make(chan string, 2)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- body.State
		if body.State == "pending" {
			started <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	reporter := newGitHubCommitStatusReporterWithCapacity("token", "", github.URL, github.Client(), 1)
	base := githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1",
	}
	first := base
	first.State = "pending"
	first.Description = "running"
	second := base
	second.State = "success"
	second.Description = "passed"
	third := base
	third.State = "failure"
	third.Description = "failed"

	if !reporter.enqueue(logger, first) {
		t.Fatal("first status was dropped")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first status request did not start")
	}
	if !reporter.enqueue(logger, second) {
		t.Fatal("second status was dropped")
	}
	if reporter.enqueue(logger, third) {
		t.Fatal("third status entered a full queue")
	}
	releaseOnce.Do(func() { close(release) })
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reporter.shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := <-received; got != "pending" {
		t.Fatalf("first state = %q, want pending", got)
	}
	if got := <-received; got != "success" {
		t.Fatalf("second state = %q, want success", got)
	}
	select {
	case got := <-received:
		t.Fatalf("dropped state was posted as %q", got)
	default:
	}
	if !strings.Contains(logs.String(), "github commit status update dropped") {
		t.Fatalf("drop was not logged: %s", logs.String())
	}
}

func TestGitHubCommitStatusReporterShutdownDrainsAcceptedStatus(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	reporter := newGitHubCommitStatusReporter("token", "", github.URL, github.Client())
	if !reporter.enqueue(slog.Default(), githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1", State: "success",
	}) {
		t.Fatal("status was dropped")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("status request did not start")
	}
	shutdownDone := make(chan error, 1)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { shutdownDone <- reporter.shutdown(shutdownCtx) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before delivery completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestServeWithDrainsGitHubCommitStatuses(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.githubCommitStatuses = newGitHubCommitStatusReporter("token", "", github.URL, github.Client())
	if !srv.githubCommitStatuses.enqueue(srv.logger, githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1", State: "success",
	}) {
		t.Fatal("status was dropped")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("status request did not start")
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- ServeWith(serveCtx, srv, "127.0.0.1:0") }()
	cancelServe()
	select {
	case err := <-serveDone:
		t.Fatalf("ServeWith returned before status delivery completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ServeWith: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeWith did not return after status delivery")
	}
}

func TestGitHubCommitStatusReporterShutdownCancelsDeliveryAtDeadline(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		canceled <- struct{}{}
		return nil, req.Context().Err()
	})}
	reporter := newGitHubCommitStatusReporter("token", "", "https://api.github.test", client)
	if !reporter.enqueue(slog.Default(), githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1", State: "success",
	}) {
		t.Fatal("status was dropped")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("status request did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := reporter.shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("status request did not observe cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGitHubCommitStatusReporterShutdownCancelsUnresolvedReservation(t *testing.T) {
	reporter := newGitHubCommitStatusReporter("token", "", "http://127.0.0.1", http.DefaultClient)
	reporter.reserve(slog.Default(), githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: "head", Pipeline: "verify", RunID: "run-1", State: "pending",
	})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := reporter.shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-reporter.done:
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not stop after reservation cancellation")
	}
}

type panickingGitHubStatusDispatcher struct {
	beforePanic func()
}

func (d panickingGitHubStatusDispatcher) Dispatch(context.Context, RunRequest) error {
	d.beforePanic()
	panic("dispatch panic")
}

func TestGitHubCommitStatusDispatchPanicReleasesReservation(t *testing.T) {
	received := make(chan string, 1)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- body.State
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	srv := New(st, nil)
	srv.githubCommitStatuses = newGitHubCommitStatusReporter("token", "", github.URL, github.Client())
	shutdownGitHubStatusReporter(t, srv.githubCommitStatuses)
	srv.WithDispatcher(panickingGitHubStatusDispatcher{beforePanic: func() {
		if !srv.githubCommitStatuses.enqueue(srv.logger, githubCommitStatus{
			Owner: "acme", Repo: "sample-app", SHA: "abc123", Pipeline: "pr-gate", RunID: "terminal", State: "success",
		}) {
			t.Error("terminal status was dropped")
		}
	}})
	body := []byte(`{
		"action":"opened",
		"number":42,
		"pull_request":{
			"head":{"ref":"feature","sha":"abc123"},
			"base":{"ref":"main","sha":"def456"},
			"user":{"login":"bob"}
		},
		"repository":{"full_name":"acme/sample-app"}
	}`)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		srv.handleGitHubPullRequest(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)),
			"pr-gate",
			"delivery-1",
			body,
		)
	}()
	if !panicked {
		t.Fatal("dispatcher panic did not propagate")
	}
	if got := waitForGitHubState(t, received); got != "success" {
		t.Fatalf("state after panic = %q, want success", got)
	}
	select {
	case got := <-received:
		t.Fatalf("rejected pending status was posted as %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGitHubCommitStatusesFollowWebhookRunLifecycle(t *testing.T) {
	received := make(chan recordedGitHubStatus, 2)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode status: %v", err)
		}
		received <- recordedGitHubStatus{Path: r.URL.Path, Body: body}
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	srv := New(st, nil).WithGitHubWebhookSecret("webhook-secret")
	srv.githubCommitStatuses = newGitHubCommitStatusReporter(
		"github-token", "https://sparkwing.example.com", github.URL, github.Client())
	shutdownGitHubStatusReporter(t, srv.githubCommitStatuses)
	controller := httptest.NewServer(srv.Handler())
	defer controller.Close()

	body := []byte(`{
		"action":"opened",
		"number":42,
		"pull_request":{
			"head":{"ref":"feature/login","sha":"1111111111111111111111111111111111111111"},
			"base":{"ref":"main","sha":"2222222222222222222222222222222222222222"},
			"user":{"login":"bob"}
		},
		"repository":{"full_name":"acme/sample-app"}
	}`)
	webhookReq, err := http.NewRequest(
		http.MethodPost,
		controller.URL+"/webhooks/github/pr-gate",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	webhookReq.Header.Set("X-GitHub-Event", "pull_request")
	webhookReq.Header.Set("X-GitHub-Delivery", "delivery-1")
	webhookReq.Header.Set("X-Hub-Signature-256", testGitHubSignature("webhook-secret", body))
	webhookResp, err := http.DefaultClient.Do(webhookReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = webhookResp.Body.Close() }()
	if webhookResp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(webhookResp.Body)
		t.Fatalf("webhook status = %d: %s", webhookResp.StatusCode, raw)
	}
	var accepted triggerResp
	if err := json.NewDecoder(webhookResp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	pending := waitForGitHubStatus(t, received)
	if pending.Body.State != "pending" || pending.Body.Context != "sparkwing/pr-gate" {
		t.Errorf("pending status = %+v", pending.Body)
	}
	if !strings.HasSuffix(pending.Path, "/statuses/1111111111111111111111111111111111111111") {
		t.Errorf("pending path = %q", pending.Path)
	}

	now := time.Now()
	if err := st.CreateRun(context.Background(), store.Run{
		ID: accepted.RunID, Pipeline: "pr-gate", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	finishResp, err := http.Post(
		controller.URL+"/api/v1/runs/"+accepted.RunID+"/finish",
		"application/json",
		strings.NewReader(`{"status":"success"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finishResp.Body.Close() }()
	if finishResp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(finishResp.Body)
		t.Fatalf("finish status = %d: %s", finishResp.StatusCode, raw)
	}
	terminal := waitForGitHubStatus(t, received)
	if terminal.Body.State != "success" || terminal.Body.Context != "sparkwing/pr-gate" {
		t.Errorf("terminal status = %+v", terminal.Body)
	}
	wantTarget := "https://sparkwing.example.com/runs?run=" + accepted.RunID
	if terminal.Body.TargetURL != wantTarget {
		t.Errorf("target_url = %q, want %q", terminal.Body.TargetURL, wantTarget)
	}
}

func TestGitHubCommitStatusFailureDoesNotRejectWebhook(t *testing.T) {
	requests := make(chan struct{}, 2)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		http.Error(w, "temporary outage", http.StatusBadGateway)
	}))
	defer github.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	logs := &lockedBuffer{}
	srv := New(st, slog.New(slog.NewTextHandler(logs, nil))).WithGitHubWebhookSecret("webhook-secret")
	srv.githubCommitStatuses = newGitHubCommitStatusReporter(
		"github-token", "", github.URL, github.Client())
	shutdownGitHubStatusReporter(t, srv.githubCommitStatuses)
	controller := httptest.NewServer(srv.Handler())
	defer controller.Close()

	body := []byte(`{
		"action":"opened",
		"number":7,
		"pull_request":{
			"head":{"ref":"feature","sha":"abc123"},
			"base":{"ref":"main","sha":"def456"},
			"user":{"login":"bob"}
		},
		"repository":{"full_name":"acme/sample-app"}
	}`)
	req, err := http.NewRequest(http.MethodPost, controller.URL+"/webhooks/github/pr-gate", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", testGitHubSignature("webhook-secret", body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want 202", resp.StatusCode)
	}
	var accepted triggerResp
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}

	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("github status request did not arrive")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "github commit status update failed") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(logs.String(), "github commit status update failed") {
		t.Fatalf("failure was not logged: %s", logs.String())
	}

	now := time.Now()
	if err := st.CreateRun(context.Background(), store.Run{
		ID: accepted.RunID, Pipeline: "pr-gate", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	finishResp, err := http.Post(
		controller.URL+"/api/v1/runs/"+accepted.RunID+"/finish",
		"application/json",
		strings.NewReader(`{"status":"failed","error":"tests failed"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finishResp.Body.Close() }()
	if finishResp.StatusCode != http.StatusNoContent {
		t.Fatalf("finish status = %d, want 204", finishResp.StatusCode)
	}
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal github status request did not arrive")
	}
	run, err := st.GetRun(context.Background(), accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.Error != "tests failed" {
		t.Errorf("run changed by status delivery failure: %+v", run)
	}
}

func TestWithGitHubCommitStatusesEmptyTokenDisablesReporting(t *testing.T) {
	srv := &Server{githubCommitStatuses: &githubCommitStatusReporter{}}
	if got := srv.WithGitHubCommitStatuses("  ", "https://sparkwing.example.com"); got != srv {
		t.Fatal("WithGitHubCommitStatuses returned a different Server")
	}
	if srv.githubCommitStatuses != nil {
		t.Fatal("empty token left status reporting enabled")
	}
	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{name: "http", base: "http://sparkwing.example.com", want: "http://sparkwing.example.com/runs?run=run-1"},
		{name: "https path", base: "https://sparkwing.example.com/team/", want: "https://sparkwing.example.com/team/runs?run=run-1"},
		{name: "relative", base: "not-an-absolute-url"},
		{name: "hostless", base: "https:///dashboard"},
		{name: "scheme", base: "ftp://sparkwing.example.com"},
		{name: "credentials", base: "https://user:password@sparkwing.example.com"},
		{name: "query", base: "https://sparkwing.example.com?team=platform"},
		{name: "empty query", base: "https://sparkwing.example.com?"},
		{name: "fragment", base: "https://sparkwing.example.com#runs"},
		{name: "empty fragment", base: "https://sparkwing.example.com#"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubRunTargetURL(tc.base, "run-1"); got != tc.want {
				t.Errorf("target URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func shutdownGitHubStatusReporter(t *testing.T, reporter *githubCommitStatusReporter) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := reporter.shutdown(ctx); err != nil {
			t.Errorf("shutdown github status reporter: %v", err)
		}
	})
}

func waitForGitHubStatus(t *testing.T, statuses <-chan recordedGitHubStatus) recordedGitHubStatus {
	t.Helper()
	select {
	case status := <-statuses:
		return status
	case <-time.After(2 * time.Second):
		t.Fatal("github status request did not arrive")
		return recordedGitHubStatus{}
	}
}

func waitForGitHubState(t *testing.T, states <-chan string) string {
	t.Helper()
	select {
	case state := <-states:
		return state
	case <-time.After(2 * time.Second):
		t.Fatal("github status request did not arrive")
		return ""
	}
}

func testGitHubSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
