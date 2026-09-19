package orchestrator_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	planLogToken = "emitted-from-plan"
	stepLogToken = "emitted-from-a-step"
)

type planLogPipe struct{ sparkwing.Base }

func (planLogPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Info(ctx, "%s", planLogToken)
	sparkwing.Job(plan, "probe", func(ctx context.Context) error {
		sparkwing.Info(ctx, "%s", stepLogToken)
		return nil
	})
	return nil
}

func init() {
	register("orch-plan-time-log", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planLogPipe{} })
}

func TestRun_InfoFromPlanReachesTheRunLog(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-plan-time-log"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	envelope, err := os.ReadFile(p.EnvelopeLog(res.RunID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if !strings.Contains(string(envelope), stepLogToken) {
		t.Fatalf("the step control never reached %s, so this run says nothing about plan-time logging",
			p.EnvelopeLog(res.RunID))
	}
	if !strings.Contains(string(envelope), planLogToken) {
		t.Fatalf("sparkwing.Info from inside Plan reached no record in %s, while the step control did; plan-time Info has no sink",
			p.EnvelopeLog(res.RunID))
	}
}

func TestRun_InfoFromPlanIsMasked(t *testing.T) {
	registerSecretArgsPipeline()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: planSecretPipeline,
		Args:     map[string]string{"token": secretArgValue, "env": visibleArgValue},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	envelope, err := os.ReadFile(p.EnvelopeLog(res.RunID))
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	body := string(envelope)
	if strings.Contains(body, secretArgValue) {
		t.Errorf("a secret arg printed from Plan reached the run log unmasked")
	}
	if !strings.Contains(body, visibleArgValue) {
		t.Fatalf("the non-secret arg printed from Plan never emitted, so the masking check proves nothing")
	}
}

const planSecretPipeline = "plan-time-secret"

type planSecretPipe struct{ sparkwing.Base }

func (planSecretPipe) Plan(ctx context.Context, plan *sparkwing.Plan, in secretArgsInputs, _ sparkwing.RunContext) error {
	sparkwing.Info(ctx, "token=%s env=%s", in.Token, in.Env)
	sparkwing.Job(plan, "noop", func(context.Context) error { return nil })
	return nil
}

func init() {
	sparkwing.Register[secretArgsInputs](planSecretPipeline,
		func() sparkwing.Pipeline[secretArgsInputs] { return &planSecretPipe{} })
}
