package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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

// WithSizingStore supplies tenant sizing persistence before Handler is constructed.
// A nil resolver leaves writes unavailable. Reads use the server's Store.ForTeam.
func (s *Server) WithSizingStore(resolve func(context.Context, store.Team) (SizingStore, error)) *Server {
	s.resolveSizingStore = resolve
	return s
}

func (s *Server) sizingStore(ctx context.Context, team string) (SizingStore, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.resolveSizingStore != nil {
		tenant, err := s.resolveSizingStore(ctx, store.Team(team))
		if err == nil && tenant == nil {
			return nil, missingEndpointPolicy()
		}
		return tenant, err
	}
	return nil, missingEndpointPolicy()
}

func (s *Server) sizingReader(ctx context.Context, team string) (SizingReader, error) {
	if s.resolveSizingStore != nil {
		return s.sizingStore(ctx, team)
	}
	if team == "" || s.store == nil {
		return nil, missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(ctx, store.Team(team))
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, engineLookupError(store.ErrNotFound)
	}
	return tenant, err
}

func (s *Server) sizingEndpoints() []Endpoint {
	return []Endpoint{
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
