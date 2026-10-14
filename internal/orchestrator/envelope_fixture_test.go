//go:build unix

package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: hosted in the test binary's own node-protocol child; each shape is one combination of memo, concurrency
// and recovery modifiers whose bodies check in at the test's gate.
type envelopeInputs struct {
	Gate   string `flag:"gate" desc:"Address of the test's gate every body checks in at"`
	Shape  string `flag:"shape" desc:"Which nodes to plan"`
	Key    string `flag:"key" desc:"Memo key of the memoized nodes"`
	Marker string `flag:"marker" desc:"Directory a recovery body records its runs in"`
	Zone   string `flag:"zone" desc:"A value the args shape records in <marker>/zone"`
}

type envelopeFixture struct{ sparkwing.Base }

// safety: a reply other than "ok" fails the node, so a test can fail a body it holds.
func envelopeBody(addr string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		who := os.Getenv("SPARKWING_RUN_ID") + "/" + sparkwing.NodeFromContext(ctx)
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return "", err
		}
		defer func() { _ = conn.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		if _, err := fmt.Fprintln(conn, who); err != nil {
			return "", err
		}
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return "", errors.Join(err, ctx.Err())
		}
		if reply = strings.TrimSpace(reply); reply != "ok" {
			return "", errors.New("the gate answered " + reply)
		}
		return "from " + who, nil
	}
}

type envelopeJob struct {
	sparkwing.Produces[string]
	body func(context.Context) (string, error)
}

func (j *envelopeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.body), nil
}

func (envelopeFixture) Plan(_ context.Context, plan *sparkwing.Plan, in envelopeInputs, _ sparkwing.RunContext) error {
	gated := func(id string) *sparkwing.JobNode {
		return sparkwing.Job(plan, id, &envelopeJob{body: envelopeBody(in.Gate)}).Resources(sparkwing.Cores(1))
	}
	key := func(context.Context) (sparkwing.CacheKey, error) { return sparkwing.CacheKey(in.Key), nil }
	group := func(name string, scope sparkwing.Scope, capacity int, onLimit sparkwing.OnLimit) *sparkwing.ConcurrencyGroup {
		return sparkwing.NewConcurrencyGroup(name, sparkwing.ConcurrencyLimit{Capacity: capacity, Scope: scope, OnLimit: onLimit})
	}
	switch in.Shape {
	case "memo":
		gated("build").Memoize(key)
		gated("twin").Memoize(key)
	case "memo-skip-lead":
		gated("lead").Memoize(key).Concurrency(group("memo-skip-grp", sparkwing.ScopeGlobal, 1, sparkwing.Skip))
	case "memo-follow":
		gated("follow").Memoize(key)
	case "box":
		gated("work").Concurrency(group("box-grp", sparkwing.ScopeBox, 1, sparkwing.Queue))
	case "run":
		gated("work").Concurrency(group("run-grp", sparkwing.ScopeRun, 1, sparkwing.Queue))
	case "cost-a":
		g := group("cost-grp", sparkwing.ScopeBox, 8, sparkwing.Queue)
		gated("a").Concurrency(g, 4)
		gated("b").Concurrency(g, 4)
	case "cost-b":
		gated("c").Concurrency(group("cost-grp", sparkwing.ScopeBox, 8, sparkwing.Queue), 4)
	case "workers":
		g := group("worker-grp", sparkwing.ScopeGlobal, 1, sparkwing.Queue)
		gated("g1").Concurrency(g)
		gated("g2").Concurrency(g)
		gated("g3").Concurrency(g)
		gated("free").Needs(gated("pre"))
	case "args":
		sparkwing.Job(plan, "record", func(context.Context) error {
			return os.WriteFile(filepath.Join(in.Marker, "zone"), []byte(in.Zone), 0o600)
		})
	case "recover-memo", "recover-skip", "recover-conc":
		addEnvelopeRecovery(plan, in, gated, key, group)
	default:
		return fmt.Errorf("unknown shape %q", in.Shape)
	}
	return nil
}

func addEnvelopeRecovery(plan *sparkwing.Plan, in envelopeInputs, gated func(string) *sparkwing.JobNode,
	key sparkwing.CacheKeyFn, group func(string, sparkwing.Scope, int, sparkwing.OnLimit) *sparkwing.ConcurrencyGroup,
) {
	rollback := func(context.Context) error {
		f, err := os.OpenFile(filepath.Join(in.Marker, "rollback"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, "body")
		return errors.Join(err, f.Close())
	}
	failing := func(context.Context) error { return errors.New("deploy failed") }
	switch in.Shape {
	case "recover-memo":
		deploy := sparkwing.Job(plan, "deploy", failing).OnFailure("rollback", rollback)
		deploy.OnFailureNode().Memoize(key)
	case "recover-skip":
		deploy := sparkwing.Job(plan, "deploy", failing).OnFailure("rollback", rollback)
		deploy.OnFailureNode().SkipIf(func(context.Context) bool { return true })
	case "recover-conc":
		g := group("recover-grp", sparkwing.ScopeRun, 1, sparkwing.Fail)
		gated("hold").Concurrency(g)
		deploy := gated("deploy").OnFailure("rollback", rollback)
		deploy.OnFailureNode().Concurrency(g)
	}
}

func init() {
	if os.Getenv(nodeProtocolEntryEnv) == "1" {
		for _, name := range []string{"envelope-a", "envelope-b"} {
			sparkwing.Register[envelopeInputs](name, func() sparkwing.Pipeline[envelopeInputs] { return envelopeFixture{} })
		}
	}
}
