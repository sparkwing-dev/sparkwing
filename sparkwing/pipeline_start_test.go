package sparkwing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
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

type childStartCapture struct {
	sdkserve.Services
	request sdkserve.ChildRequest
}

func (s *childStartCapture) StartChild(_ context.Context, request sdkserve.ChildRequest) (string, error) {
	s.request = request
	return "detached", nil
}

func TestStartChildRequestsDetachedSameRevisionLineage(t *testing.T) {
	host := &childStartCapture{}
	id, err := sparkwing.StartChild(sdkserve.WithServices(t.Context(), host), "scan", nil)
	if err != nil || id != "detached" || !host.request.Detached || host.request.Repo != "" || host.request.Branch != "" || host.request.Pipeline != "scan" {
		t.Fatalf("child request: %+v, %q, %v", host.request, id, err)
	}
}
