package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// metricWriter rechecks tenant, ancestry and reporting fences in each committing transaction.
type metricWriter interface {
	Team() store.Team
	WriteMetric(context.Context, capability.Authorized, store.MetricSample) error
	WriteUsage(context.Context, capability.Authorized, store.NodeUsage) error
}

func (s *Server) metricWriter(ctx context.Context, team string) (metricWriter, error) {
	return tenantPort[metricWriter](ctx, s, store.Team(team))
}

var _ metricWriter = (*store.Tenant)(nil)
