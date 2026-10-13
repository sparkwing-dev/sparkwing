package sdkserve_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeclient"
	"github.com/sparkwing-dev/sparkwing/internal/nodeclient/nodeclienttest"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
)

const routesPipeline = "sdkserve-routes-fixture"

// safety: the cleanup step's argv appends here, so the test can see what the
// host ran after the process ended.
var cleanupLog string

type digest struct {
	Digest string `json:"digest"`
}

type buildJob struct {
	sparkwing.Base
	sparkwing.Produces[digest]
}

func (*buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (digest, error) { return digest{"local"}, nil }), nil
}

type deployJob struct {
	sparkwing.Base
	sparkwing.Produces[string]
	Build   sparkwing.Ref[digest]
	Pending sparkwing.Ref[digest]
	Tag     sparkwing.Ref[string]
}

func check(cond bool, format string, args ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, args...)
}

func (j *deployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "secret", func(ctx context.Context) error {
		v, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
		if err != nil || v != "hunter2" {
			return fmt.Errorf("secret = %q: %w", v, err)
		}
		_, err = sparkwing.Secret(ctx, "UNSET")
		return check(errors.Is(err, sparkwing.ErrSecretMissing), "unset secret = %v, want ErrSecretMissing", err)
	})
	sparkwing.Step(w, "pending", func(ctx context.Context) error {
		_, ok := j.Pending.TryGet(ctx)
		return check(!ok, "a node that has not finished read as present")
	})
	sparkwing.Step(w, "last", func(ctx context.Context) error {
		tag := j.Tag.Get(ctx)
		return check(tag == "v1.2.3", "last run tag = %q", tag)
	})
	sparkwing.Step(w, "await", func(ctx context.Context) error {
		out, err := sparkwing.RunAndAwait[digest, sparkwing.NoInputs](ctx, "build", "build", sparkwing.WithFreshTimeout(time.Minute))
		return check(err == nil && out.Digest == "child", "await = %+v, %v", out, err)
	})
	sparkwing.Step(w, "oidc", func(ctx context.Context) error {
		tok, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com")
		return check(err == nil && tok == "eyJ.signed.jwt", "oidc = %q, %v", tok, err)
	})
	sparkwing.Step(w, "cleanup", func(ctx context.Context) error {
		if _, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"sh", "-c", "echo kept >> " + cleanupLog}}); err != nil {
			return err
		}
		release, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"sh", "-c", "echo released >> " + cleanupLog}})
		release()
		return err
	})
	return sparkwing.Step(w, "refs", func(ctx context.Context) (string, error) { return j.Build.Get(ctx).Digest, nil }), nil
}

type routesFixture struct{ sparkwing.Base }

func (routesFixture) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &buildJob{})
	pending := sparkwing.Job(plan, "pending", &buildJob{})
	sparkwing.Job(plan, "deploy", &deployJob{
		Build:   sparkwing.RefTo[digest](build),
		Pending: sparkwing.RefTo[digest](pending),
		Tag:     sparkwing.RefToLastRun[string]("release", "tag"),
	})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs](routesPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return routesFixture{} })
}

type session struct {
	t      *testing.T
	in     *io.PipeWriter
	frames *nodeproto.Reader
	next   int
}

func serve(t *testing.T, c *nodeclient.Client) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	cfg := sdkserve.Config{
		Node: c,
		PlanDoc: func(*sparkwing.Plan, sparkwing.RunContext, *sparkwing.Registration) (nodeproto.PlanDoc, error) {
			return nodeproto.PlanDoc{}, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- sdkserve.Serve(context.Background(), inR, outW, cfg) }()
	t.Cleanup(func() {
		_ = inW.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
		_ = outR.Close()
	})
	return &session{t: t, in: inW, frames: nodeproto.NewReader(outR)}
}

func (s *session) call(req nodeproto.Request) nodeproto.Reply {
	s.t.Helper()
	s.next++
	req.ID = fmt.Sprint(s.next)
	line, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
	for {
		f, err := s.frames.Next()
		if err != nil {
			s.t.Fatal(err)
		}
		if f.Kind == nodeproto.KindReply {
			return f.Reply
		}
	}
}

func (s *session) run(op nodeproto.Op, node, step string) nodeproto.RunResult {
	s.t.Helper()
	var res nodeproto.RunResult
	if err := s.call(nodeproto.Request{Op: op, Node: node, Step: step, Attempt: 1}).Decode(&res); err != nil {
		s.t.Fatalf("%s %s/%s: %v", op, node, step, err)
	}
	return res
}

func TestStepsReachTheHostThroughTheNodeRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cleanup argv is a POSIX shell")
	}
	cleanupLog = filepath.Join(t.TempDir(), "cleanups")
	h := nodeclienttest.Start(t, "deploy", map[string]nodeclienttest.Secret{"DEPLOY_TOKEN": {Value: "hunter2", Masked: true}}, "UNSET")
	answer := nodeclienttest.Answer
	h.Handle("GET /api/v1/runs/run-1/nodes/build", answer(http.StatusOK, map[string]string{"status": "done", "outcome": "success"}))
	h.Handle("GET /api/v1/runs/run-1/nodes/build/output", answer(http.StatusOK, h.Object("build", []byte(`{"digest":"remote"}`))))
	h.Handle("GET /api/v1/runs/run-1/nodes/pending", answer(http.StatusOK, map[string]string{"status": "running"}))
	h.Handle("POST /api/v1/runs/run-1/nodes/deploy/claim/input",
		answer(http.StatusOK, store.ClaimInput{RunID: "run-0", NodeID: "tag", Output: h.Object("tag", []byte(`"v1.2.3"`))}))
	h.Handle("POST /api/v1/runs/run-1/children", answer(http.StatusAccepted, map[string]string{"run_id": "child-1"}))
	h.Handle("GET /api/v1/runs/run-1/children/child-1", answer(http.StatusOK, map[string]string{"status": "success"}))
	h.Handle("GET /api/v1/runs/run-1/children/child-1/nodes/build/output", answer(http.StatusOK, h.Object("child", []byte(`{"digest":"child"}`))))
	h.Handle("POST /api/v1/runs/run-1/oidc-token", answer(http.StatusOK, map[string]any{"token": "eyJ.signed.jwt", "expires_at": time.Unix(1e9, 0).UTC()}))
	nodeclient.Bind(h.Client)

	s := serve(t, h.Client)
	plan := nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: routesPipeline, Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: nodeclienttest.RunID}}
	if reply := s.call(plan); !reply.OK {
		t.Fatalf("plan = %+v", reply.Error)
	}
	for _, step := range []string{"secret", "pending", "last", "await", "oidc", "cleanup"} {
		if res := s.run(nodeproto.OpRunStep, "deploy", step); res.Outcome != nodeproto.OutcomeSuccess {
			t.Errorf("step %s = %+v", step, res)
		}
	}
	if res := s.run(nodeproto.OpRunStep, "deploy", "refs"); string(res.Output) != `"remote"` {
		t.Errorf("before build ran here, refs = %+v, want the host's output", res)
	}
	if res := s.run(nodeproto.OpRunNode, "build", ""); res.Outcome != nodeproto.OutcomeSuccess {
		t.Fatalf("build = %+v", res)
	}
	if res := s.run(nodeproto.OpRunStep, "deploy", "refs"); string(res.Output) != `"local"` {
		t.Errorf("after build ran here, refs = %+v, want this process's output", res)
	}

	h.Revoke()
	if err := h.Server.RunCleanups(context.Background(), h.Bearer, nodehost.CleanupScope{Env: []string{"PATH=" + os.Getenv("PATH")}}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(cleanupLog); err != nil || string(got) != "kept\n" {
		t.Errorf("host ran cleanups %q (%v), want only the unreleased one", got, err)
	}
	if strings.Contains(h.Logs.String(), h.Bearer) {
		t.Errorf("host logs leak the bearer:\n%s", h.Logs.String())
	}
}
