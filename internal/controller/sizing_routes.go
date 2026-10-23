package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SizingReader supplies tenant-scoped sizing without exposing another persistence owner.
type SizingReader interface {
	GetPipelineProfile(context.Context, string, string) (*store.PipelineProfile, error)
}

// SizingStore commits measurements after rechecking authority and the sealed
// repository/pipeline binding in the same transaction. Exact retries must not
// duplicate measurements. Tenant's unfenced measurement methods cannot satisfy it.
type SizingStore interface {
	SizingReader
	SetProfilePin(context.Context, capability.Authorized, float64, int64) error
	RecordProfileObservation(context.Context, capability.Authorized, store.ProfileObservation) error
	RecordContention(context.Context, capability.Authorized) error
	RecordWaitObservation(context.Context, capability.Authorized, time.Duration) error
}

func (s *Server) sizingStore(ctx context.Context, team string) (SizingStore, error) {
	return tenantPort[SizingStore](ctx, s, store.Team(team))
}

func (s *Server) sizingReader(ctx context.Context, team string) (SizingReader, error) {
	return tenantPort[SizingReader](ctx, s, store.Team(team))
}

func (s *Server) sizingEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/pipelines/{name}/sizing", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleGetPipelineProfile},
		{Method: "PUT", Pattern: "/api/v1/pipelines/{name}/sizing/pin", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleSetPipelinePin},
		{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/observations", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleRecordProfileObservation},
		{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/contention", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleRecordContention},
		{Method: "POST", Pattern: "/api/v1/pipelines/{name}/sizing/waits", Authentication: capability.AuthCapability, Decode: sizingTarget, Handle: s.handleRecordWaitObservation},
	}
}

func sizingTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{Pipeline: r.PathValue("name"), NodeID: r.URL.Query().Get("job")}}, nil
}

var _ SizingReader = (*store.Tenant)(nil)
