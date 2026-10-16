package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// MetricWriter rechecks tenant, ancestry and reporting fences in each committing transaction.
type MetricWriter interface {
	Team() store.Team
	WriteMetric(context.Context, capability.Authorized, store.MetricSample) error
	WriteUsage(context.Context, capability.Authorized, store.NodeUsage) error
}

// WithMetricWriter supplies the tenant's committing metric and usage transactions.
func (s *Server) WithMetricWriter(resolve func(context.Context, store.Team) (MetricWriter, error)) *Server {
	s.resolveMetricWriter = resolve
	return s
}

func (s *Server) metricWriter(ctx context.Context, team string) (MetricWriter, error) {
	var writer MetricWriter
	var err error
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.resolveMetricWriter != nil {
		writer, err = s.resolveMetricWriter(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		if err == nil {
			writer = tenant
		}
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || string(writer.Team()) != team {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

var _ MetricWriter = (*store.Tenant)(nil)
