package nodeclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/nodeclient/nodeclienttest"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var answer = nodeclienttest.Answer

func nodeRow(status, outcome string) http.HandlerFunc {
	return answer(http.StatusOK, map[string]string{"status": status, "outcome": outcome})
}

// safety: every error a test sees passes through here, so none may quote the
// bearer or the controller's credential.
func noSecrets(t *testing.T, h *nodeclienttest.Host, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if msg := err.Error(); strings.Contains(msg, h.Bearer) || strings.Contains(msg, nodeclienttest.ClaimToken) {
		t.Fatalf("error leaks a credential: %s", msg)
	}
}

func noLeakInLogs(t *testing.T, h *nodeclienttest.Host) {
	t.Helper()
	if l := h.Logs.String(); strings.Contains(l, h.Bearer) || strings.Contains(l, nodeclienttest.ClaimToken) {
		t.Fatalf("host logs leak a credential:\n%s", l)
	}
}

func wantCode(t *testing.T, h *nodeclienttest.Host, err error, code string) {
	t.Helper()
	noSecrets(t, h, err)
	if !nodeclient.HasCode(err, code) {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestEveryRouteAnswers(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", map[string]nodeclienttest.Secret{
		"DEPLOY_TOKEN": {Value: "hunter2", Masked: true},
		"REGION":       {Value: "us-east-1"},
	}, "UNSET")
	ctx := context.Background()
	c := h.Client
	build := h.Object("build", []byte(`{"digest":"sha256:abc"}`))
	h.Handle("GET /api/v1/runs/run-1/nodes/{node}", nodeRow("done", "success"))
	h.Handle("GET /api/v1/runs/run-1/nodes/{node}/output", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("node") != "build/linux" {
			t.Errorf("output of %q, want build/linux", r.PathValue("node"))
		}
		answer(http.StatusOK, build)(w, r)
	})
	var input store.ClaimInputRequest
	h.Handle("POST /api/v1/runs/run-1/nodes/deploy/claim/input", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&input)
		answer(http.StatusOK, store.ClaimInput{RunID: "run-0", NodeID: "tag", Output: h.Object("tag", []byte(`"v1.2.3"`))})(w, r)
	})
	h.Handle("POST /api/v1/runs/run-1/oidc-token", answer(http.StatusOK, map[string]any{"token": "eyJ.signed.jwt", "expires_at": time.Unix(1e9, 0).UTC()}))
	var child map[string]any
	h.Handle("POST /api/v1/runs/run-1/children", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&child)
		answer(http.StatusAccepted, map[string]string{"run_id": "child-1"})(w, r)
	})
	h.Handle("GET /api/v1/runs/run-1/children/child-1", answer(http.StatusOK, map[string]string{"status": "success"}))
	h.Handle("GET /api/v1/runs/run-1/children/child-1/nodes/{node}/output", answer(http.StatusOK, h.Object("child", []byte(`{"image":"app:1"}`))))

	if v, masked, err := c.Secret(ctx, "DEPLOY_TOKEN"); err != nil || v != "hunter2" || !masked {
		t.Fatalf("secret = %q %v %v", v, masked, err)
	}
	if v, masked, err := c.Secret(ctx, "REGION"); err != nil || v != "us-east-1" || masked {
		t.Fatalf("config = %q %v %v", v, masked, err)
	}
	_, _, err := c.Secret(ctx, "UNSET")
	wantCode(t, h, err, nodeclient.CodeSecretNotFound)

	if out, err := c.NodeOutput(ctx, "build/linux"); err != nil || string(out) != `{"digest":"sha256:abc"}` {
		t.Fatalf("output = %s %v", out, err)
	}
	run, out, err := c.PipelineLatest(ctx, "release", "tag", 5*time.Second)
	if err != nil || run != "run-0" || string(out) != `"v1.2.3"` {
		t.Fatalf("latest = %s %s %v", run, out, err)
	}
	if input.Pipeline != "release" || input.Node != "tag" || input.MaxAgeMS != 5000 {
		t.Fatalf("latest forwarded %+v", input)
	}
	if tok, exp, err := c.OIDCToken(ctx, "sts.amazonaws.com"); err != nil || tok != "eyJ.signed.jwt" || exp.IsZero() {
		t.Fatalf("oidc = %q %v %v", tok, exp, err)
	}

	id, err := c.StartChild(ctx, nodeclient.ChildRequest{Pipeline: "build", Args: map[string]string{"tag": "1"}})
	if err != nil || id != "child-1" || child["pipeline"] != "build" {
		t.Fatalf("start = %q %v, forwarded %v", id, err, child)
	}
	st, err := c.AwaitChild(ctx, id, "build", time.Second)
	if err != nil || st.Status != "success" || string(st.Output) != `{"image":"app:1"}` {
		t.Fatalf("await = %+v %v", st, err)
	}
	noLeakInLogs(t, h)
}

func TestCleanupsRunUnlessReleased(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cleanup argv is a POSIX shell")
	}
	h := nodeclienttest.Start(t, "deploy", nil)
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "ran")
	if _, err := h.Client.RegisterCleanup(ctx, []string{"sh", "-c", "echo kept >> " + out}, "kept"); err != nil {
		t.Fatal(err)
	}
	id, err := h.Client.RegisterCleanup(ctx, []string{"sh", "-c", "echo released >> " + out}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Client.ReleaseCleanup(ctx, id); err != nil {
		t.Fatal(err)
	}
	wantCode(t, h, h.Client.ReleaseCleanup(ctx, id), "unknown_cleanup")

	h.Revoke()
	if err := h.Server.RunCleanups(ctx, h.Bearer, nodehost.CleanupScope{Env: []string{"PATH=" + os.Getenv("PATH")}}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "kept\n" {
		t.Fatalf("cleanups wrote %q (%v), want only the kept one", got, err)
	}
}

func TestRefusalsMapToTheirCodes(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", nil)
	ctx := context.Background()
	c := h.Client
	h.Handle("GET /api/v1/runs/run-1/nodes/pending", nodeRow("running", ""))
	h.Handle("GET /api/v1/runs/run-1/nodes/failed", nodeRow("done", "failed"))
	h.Handle("GET /api/v1/runs/run-1/nodes/sibling", nodeRow("done", "success"))
	h.Handle("GET /api/v1/runs/run-1/nodes/sibling/output", answer(http.StatusForbidden, map[string]string{"error": "output_not_ancestor"}))
	h.Handle("POST /api/v1/runs/run-1/nodes/deploy/claim/input", func(w http.ResponseWriter, r *http.Request) {
		var req store.ClaimInputRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Pipeline == "undeclared" {
			answer(http.StatusForbidden, map[string]string{"error": "input_undeclared"})(w, r)
			return
		}
		answer(http.StatusNotFound, map[string]string{"error": "not_found"})(w, r)
	})
	h.Handle("POST /api/v1/runs/run-1/oidc-token", answer(http.StatusNotFound, map[string]string{"error": "no_key"}))

	_, _, err := c.Secret(ctx, "NOT_DECLARED")
	wantCode(t, h, err, "secret_undeclared")
	_, err = c.NodeOutput(ctx, "missing")
	wantCode(t, h, err, nodeclient.CodeUnknownNode)
	_, err = c.NodeOutput(ctx, "pending")
	wantCode(t, h, err, nodeclient.CodeNotReady)
	_, err = c.NodeOutput(ctx, "failed")
	wantCode(t, h, err, nodeclient.CodeNoOutput)
	_, err = c.NodeOutput(ctx, "sibling")
	wantCode(t, h, err, "not_dependency")
	_, _, err = c.PipelineLatest(ctx, "release", "tag", 0)
	wantCode(t, h, err, nodeclient.CodeNoRun)
	_, _, err = c.PipelineLatest(ctx, "undeclared", "tag", 0)
	wantCode(t, h, err, "reference_undeclared")
	_, _, err = c.OIDCToken(ctx, "sts")
	wantCode(t, h, err, nodeclient.CodeOIDCUnavailable)
	_, err = c.AwaitChild(ctx, "not-mine", "", 0)
	wantCode(t, h, err, "unknown_child")

	stranger, err := nodeclient.New(h.Server.URL(), "not-a-bearer")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = stranger.Secret(ctx, "X")
	wantCode(t, h, err, "unauthorized")
	if strings.Contains(err.Error(), "not-a-bearer") {
		t.Fatalf("error quotes the bearer: %v", err)
	}

	h.Revoke()
	_, _, err = c.Secret(ctx, "X")
	wantCode(t, h, err, "unauthorized")
	noLeakInLogs(t, h)
}

func TestUpstreamUnavailableIsRetriedThenReported(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", nil)
	var calls atomic.Int32
	h.Handle("POST /api/v1/runs/run-1/oidc-token", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answer(http.StatusInternalServerError, map[string]string{"error": "boom " + nodeclienttest.ClaimToken})(w, r)
	})
	_, _, err := h.Client.OIDCToken(context.Background(), "sts")
	wantCode(t, h, err, nodeclient.CodeUpstreamUnavailable)
	if n := calls.Load(); n != 3 {
		t.Fatalf("controller asked %d times, want 3", n)
	}
	noLeakInLogs(t, h)
}

// safety: the proxy reports each 429 the host answers, so the test knows the
// client saw the limit before it frees a slot.
func limitedClient(t *testing.T, h *nodeclienttest.Host, refused chan<- struct{}) *nodeclient.Client {
	t.Helper()
	target, err := url.Parse(h.Server.URL())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) },
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusTooManyRequests {
				select {
				case refused <- struct{}{}:
				default:
				}
			}
			return nil
		},
	})
	t.Cleanup(proxy.Close)
	c, err := nodeclient.New(proxy.URL, h.Bearer)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTooManyRequestsIsRetriedUntilASlotFrees(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", nil)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	h.Handle("POST /api/v1/runs/run-1/oidc-token", func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		answer(http.StatusOK, map[string]any{"token": "tok", "expires_at": time.Unix(1e9, 0).UTC()})(w, r)
	})
	ctx := context.Background()
	held := make(chan error, 4)
	for range 4 {
		go func() { _, _, err := h.Client.OIDCToken(ctx, "sts"); held <- err }()
	}
	for range 4 {
		<-entered
	}

	refused := make(chan struct{}, 1)
	c := limitedClient(t, h, refused)
	cancelled, cancel := context.WithCancel(ctx)
	gaveUp := make(chan error, 1)
	go func() { _, _, err := c.OIDCToken(cancelled, "sts"); gaveUp <- err }()
	<-refused
	cancel()
	err := <-gaveUp
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry = %v, want the cancellation", err)
	}
	select {
	case <-refused:
	default:
	}

	got := make(chan error, 1)
	go func() { _, _, err := c.OIDCToken(ctx, "sts"); got <- err }()
	<-refused
	free()
	if err := <-got; err != nil {
		t.Fatalf("retried call = %v", err)
	}
	for range 4 {
		if err := <-held; err != nil {
			t.Fatal(err)
		}
	}
	noLeakInLogs(t, h)
}

func TestFromProcessIsNilOutsideTheProtocol(t *testing.T) {
	t.Setenv(protofd.NodeURLEnv, "http://127.0.0.1:1")
	t.Setenv(protofd.NodeTokenEnv, "swn_secret")
	if c, err := nodeclient.FromProcess(); c != nil || err != nil {
		t.Fatalf("FromProcess in a process not started with %s = %v, %v", protofd.Arg, c, err)
	}
}

func TestOnlyLoopbackURLsGetTheBearer(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:41873", "http://[::1]:41873", "http://localhost:41873/"} {
		if _, err := nodeclient.New(raw, "swn_secret"); err != nil {
			t.Errorf("New(%s) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"https://127.0.0.1:41873", "http://127.0.0.1", "http://127.0.0.2:41873", "http://10.0.0.1:80",
		"http://node.example.com:80", "http://user:hunter2@127.0.0.1:41873", "http://127.0.0.1:41873/api",
		"http://127.0.0.1:41873?x=1", "http://127.0.0.1:0", "127.0.0.1:41873",
	} {
		_, err := nodeclient.New(raw, "swn_secret")
		if err == nil || !strings.Contains(err.Error(), "loopback") || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("New(%s) = %v, want a refusal that names loopback and quotes no URL", raw, err)
		}
	}
}

func TestRedirectsAreNeverFollowed(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("the bearer followed a redirect")
		}
	}))
	t.Cleanup(other.Close)
	redirector := httptest.NewServer(http.RedirectHandler(other.URL+"/steal", http.StatusTemporaryRedirect))
	t.Cleanup(redirector.Close)
	c, err := nodeclient.New(redirector.URL, "swn_secret")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Secret(context.Background(), "X")
	var e *nodeclient.Error
	if !errors.As(err, &e) || e.Status != http.StatusTemporaryRedirect || strings.Contains(err.Error(), "swn_secret") {
		t.Fatalf("redirected read = %v, want a 307 refusal", err)
	}
	if reached.Load() != 0 {
		t.Fatal("the client followed the redirect")
	}
}

func TestChildStartIsNotRetriedOn502(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", nil)
	var calls atomic.Int32
	h.Handle("POST /api/v1/runs/run-1/children", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answer(http.StatusInternalServerError, map[string]string{"error": "lost"})(w, r)
	})
	_, err := h.Client.StartChild(context.Background(), nodeclient.ChildRequest{Pipeline: "build"})
	wantCode(t, h, err, nodeclient.CodeUpstreamUnavailable)
	if n := calls.Load(); n != 1 {
		t.Fatalf("controller asked %d times, want 1: a retry may start a second child", n)
	}
}

func TestPersistent429EndsAtTheRetryBudget(t *testing.T) {
	h := nodeclienttest.Start(t, "deploy", nil)
	h.FillForwardSlots(t)
	c, err := nodeclient.New(h.Server.URL(), h.Bearer)
	if err != nil {
		t.Fatal(err)
	}
	nodeclient.SetRetryBudget(c, 200*time.Millisecond)
	_, err = c.StartChild(context.Background(), nodeclient.ChildRequest{Pipeline: "build"})
	wantCode(t, h, err, nodeclient.CodeTooManyRequests)
}

func TestA429RetryThatStallsEndsAtTheRetryBudget(t *testing.T) {
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			<-r.Context().Done()
			return
		}
		answer(http.StatusTooManyRequests, map[string]string{"error": "slots full", "code": nodeclient.CodeTooManyRequests})(w, r)
	}))
	t.Cleanup(host.Close)
	c, err := nodeclient.New(host.URL, "bearer")
	if err != nil {
		t.Fatal(err)
	}
	nodeclient.SetRetryBudget(c, 200*time.Millisecond)
	_, _, err = c.Secret(context.Background(), "X")
	if !nodeclient.HasCode(err, nodeclient.CodeTooManyRequests) {
		t.Fatalf("err = %v, want the 429 the budget ended on", err)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("host asked %d times, want a retry", n)
	}
}

func TestTheRetryBudgetStartsAtTheFirstRefusal(t *testing.T) {
	var calls atomic.Int32
	var clock atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// safety: the refusal arrives an hour into the call, far past the budget.
			clock.Add(int64(time.Hour))
			answer(http.StatusTooManyRequests, map[string]string{"error": "slots full", "code": nodeclient.CodeTooManyRequests})(w, r)
			return
		}
		answer(http.StatusOK, map[string]any{"value": "ready", "masked": true})(w, r)
	}))
	t.Cleanup(host.Close)
	c, err := nodeclient.New(host.URL, "bearer")
	if err != nil {
		t.Fatal(err)
	}
	nodeclient.SetRetryBudget(c, 2*time.Minute)
	nodeclient.SetClock(c, func() time.Time { return time.Unix(0, clock.Load()) })
	value, _, err := c.Secret(context.Background(), "X")
	if err != nil || value != "ready" || calls.Load() != 2 {
		t.Fatalf("value %q err %v after %d requests, want the retry to succeed", value, err, calls.Load())
	}
}
