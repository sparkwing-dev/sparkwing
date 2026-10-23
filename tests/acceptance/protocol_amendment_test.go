package acceptance_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func testGoSDKRefusesEmptyOIDCToken(t *testing.T) {
	recordLeaf(t)
	host := sparkwingtest.New(sparkwingtest.Options{
		OIDCToken: func(context.Context, string) (string, error) { return "", nil },
	})
	t.Cleanup(func() { _ = host.Close() })
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "token", func(ctx context.Context) error {
		_, err := sparkwing.OIDCToken(ctx, "audience")
		return err
	})
	if _, err := host.RunStep(t.Context(), plan, "token", "run"); err == nil {
		t.Fatal("empty OIDC token reported success")
	}
	if len(host.Masks()) != 0 {
		t.Fatal("empty token changed the mask registry")
	}
}

func testGoSDKRefusesCollidingMasks(t *testing.T) {
	for _, kind := range []string{"job", "step", "group", "concurrency", "approval", "short"} {
		t.Run(kind, func(t *testing.T) {
			recordLeaf(t)
			host := sparkwingtest.New(sparkwingtest.Options{})
			t.Cleanup(func() {
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
			})
			plan := sparkwing.NewPlan()
			secret := "mask-value"
			if kind == "short" {
				secret = "xy"
			}
			id := "prefix-" + secret
			job, step := "probe", "run"
			if kind == "job" || kind == "short" {
				job = id
			}
			body := func(ctx context.Context) error { return sparkwing.Mask(ctx, secret) }
			if kind == "step" {
				step = id
				sparkwing.Job(plan, job, maskStep{id: step, body: body})
			} else {
				node := sparkwing.Job(plan, job, body)
				switch kind {
				case "group":
					sparkwing.GroupJobs(plan, id, node)
				case "concurrency":
					node.Concurrency(sparkwing.NewConcurrencyGroup(id, sparkwing.ConcurrencyLimit{Capacity: 1}))
				case "approval":
					plan.Approval(id, sparkwing.ApprovalConfig{})
				}
			}
			_, err := host.RunStep(t.Context(), plan, job, step)
			if kind == "short" {
				if err == nil || !strings.Contains(err.Error(), "plain config variable") || strings.Contains(err.Error(), secret) {
					t.Fatalf("short mask = %v", err)
				}
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier || strings.Contains(err.Error(), secret) {
					t.Fatalf("colliding mask = %v", err)
				}
			}
			if len(host.Masks()) != 0 {
				t.Fatal("refused registration changed the mask registry")
			}
		})
	}
}

func testGoSDKApprovalCondition(t *testing.T) {
	recordLeaf(t)
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Fatal(err)
		}
	})
	plan := sparkwing.NewPlan()
	plan.Approval("approval", sparkwing.ApprovalConfig{}).SkipIf(func(context.Context) bool { return true })
	doc := sdkserve.DefaultOperations.Document(plan)
	if len(doc.Jobs) != 1 || len(doc.Jobs[0].Steps) != 0 || len(doc.Jobs[0].Closures.SkipIf) != 1 {
		t.Fatalf("approval declaration = %+v", doc)
	}
	value, err := host.Eval(t.Context(), plan, "approval", doc.Jobs[0].Closures.SkipIf[0], nil)
	if err != nil || string(value) != "true" {
		t.Fatalf("approval condition = %s, %v", value, err)
	}
}

type maskStep struct {
	id   string
	body func(context.Context) error
}

func (s maskStep) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, s.id, s.body), nil
}

func testGoSDKMaskRegistryGuardsLaterDeclarations(t *testing.T) {
	for _, registrationFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "mask-first", false: "expansion-first"}[registrationFirst], func(t *testing.T) {
			recordLeaf(t)
			host := sparkwingtest.New(sparkwingtest.Options{})
			t.Cleanup(func() {
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
			})
			plan := sparkwing.NewPlan()
			source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
			generator := source.Generate(func(ctx context.Context, expansion *sparkwing.Plan) error {
				sparkwing.Info(ctx, "before declaration mask-value")
				sparkwing.Job(expansion, "child-mask-value", func(context.Context) error { return nil })
				return nil
			})
			if registrationFirst {
				if err := sparkwing.Mask(host.Context(t.Context()), "mask-value"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := host.Eval(t.Context(), plan, "source", generator.ID(), nil)
			if registrationFirst {
				if err == nil || err.Error() != "generator declaration failed" {
					t.Fatalf("failed generator = %v", err)
				}
				if len(host.Records()) != 0 {
					t.Fatalf("failed declarations released output: %+v", host.Records())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = sparkwing.Mask(host.Context(t.Context()), "mask-value")
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.SecretInIdentifier || strings.Contains(err.Error(), "mask-value") {
				t.Fatalf("late collision = %v", err)
			}
		})
	}
	host := sparkwingtest.New(sparkwingtest.Options{})
	defer host.Close()
	if err := sparkwing.Mask(host.Context(t.Context()), "mask-value"); err != nil {
		t.Fatal(err)
	}
	invoked := false
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "job-mask-value", func(context.Context) error { invoked = true; return nil })
	if _, err := host.RunStep(t.Context(), plan, "job-mask-value", "run"); err == nil || invoked {
		t.Fatalf("later secret identifier ran: %t, %v", invoked, err)
	}
}
