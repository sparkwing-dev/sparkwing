package sparkwingtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestStartChildReturnsAdmissionWithoutAwaiting(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{StartChild: func(_ context.Context, pipeline string, args map[string]string) (string, error) {
		if pipeline != "scan" || args["target"] != "staging" {
			t.Fatalf("child request: %s, %v", pipeline, args)
		}
		return "child-run", nil
	}})
	defer host.Close()
	run, err := sparkwing.StartChild(host.Context(t.Context()), "scan", map[string]string{"target": "staging"})
	if err != nil || run != "child-run" {
		t.Fatalf("start = %q, %v", run, err)
	}
}

func TestStartChildPropagatesAdmissionFailure(t *testing.T) {
	refusal := errors.New("child was not declared")
	host := sparkwingtest.New(sparkwingtest.Options{StartChild: func(context.Context, string, map[string]string) (string, error) { return "", refusal }})
	defer host.Close()
	if _, err := sparkwing.StartChild(host.Context(t.Context()), "scan", nil); !errors.Is(err, refusal) {
		t.Fatalf("lost refusal: %v", err)
	}
	if _, err := sparkwing.StartChild(t.Context(), "scan", nil); err == nil {
		t.Fatal("started a child outside a pipeline")
	}
}

func TestAwaitChildReturnsObservedFailureAndRefusesMissingServices(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{AwaitChild: func(_ context.Context, run string) (string, error) {
		if run != "scan" {
			t.Fatalf("awaited %q", run)
		}
		return "failed", nil
	}})
	defer host.Close()
	if status, err := sparkwing.AwaitChild(host.Context(t.Context()), "scan"); err != nil || status != "failed" {
		t.Fatalf("await = %q, %v", status, err)
	}
	if _, err := sparkwing.AwaitChild(t.Context(), "scan"); err == nil {
		t.Fatal("missing host services accepted")
	}
}
