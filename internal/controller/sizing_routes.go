package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// sizingReader supplies tenant-scoped sizing without exposing another persistence owner.
type sizingReader interface {
	GetPipelineProfile(context.Context, string, string) (*store.PipelineProfile, error)
}

func (s *Server) sizingReader(ctx context.Context, team string) (sizingReader, error) {
	return tenantPort[sizingReader](ctx, s, store.Team(team))
}

func (s *Server) sizingEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/pipelines/{name}/sizing", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleGetPipelineProfile},
	}
}

func sizingTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{Pipeline: r.PathValue("name"), NodeID: r.URL.Query().Get("job")}}, nil
}

var _ sizingReader = (*store.Tenant)(nil)
