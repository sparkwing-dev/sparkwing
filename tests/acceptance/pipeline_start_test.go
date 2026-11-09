package acceptance_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

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
