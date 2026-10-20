package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// MetricWriter rechecks tenant, ancestry and reporting fences in each committing transaction.
type MetricWriter interface {
	Team() store.Team
	WriteMetric(context.Context, capability.Authorized, store.MetricSample) error
	WriteUsage(context.Context, capability.Authorized, store.NodeUsage) error
}

func (s *Server) metricWriter(ctx context.Context, team string) (MetricWriter, error) {
	return tenantPort[MetricWriter](ctx, s, store.Team(team))
}

var _ MetricWriter = (*store.Tenant)(nil)
