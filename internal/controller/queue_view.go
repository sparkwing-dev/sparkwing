package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// QueueReader supplies the concurrency groups visible to one authorized team.
// Its methods preserve Tenant's read contract without exposing deployment-wide reads.
type QueueReader interface {
	Team() store.Team
	ListConcurrencyStates(context.Context) ([]*store.ConcurrencyState, error)
}

// WithQueueReader supplies tenant queue reads before Handler is constructed.
// A nil resolver uses Store.ForTeam. The reader must belong to the authorized team.
func (s *Server) WithQueueReader(resolve func(context.Context, store.Team) (QueueReader, error)) *Server {
	s.resolveQueueReader = resolve
	return s
}

func (s *Server) queueReader(ctx context.Context, team string) (QueueReader, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	var reader QueueReader
	var err error
	if s.resolveQueueReader != nil {
		reader, err = s.resolveQueueReader(ctx, store.Team(team))
	} else if s.store != nil {
		reader, err = s.store.ForTeam(ctx, store.Team(team))
	} else {
		return nil, missingEndpointPolicy()
	}
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, engineLookupError(store.ErrNotFound)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return reader, nil
}

func (s *Server) queueEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "PUT", Pattern: "/api/v1/queue/groups/{group}/priority", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleQueuePriority},
		{Method: "GET", Pattern: "/api/v1/queue", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleQueueStateView},
	}
}

func (s *Server) handleQueueStateView(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.queueReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	states, err := tenant.ListConcurrencyStates(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	now := time.Now()
	qs := wingwire.QueueState{}
	for _, st := range states {
		if st == nil {
			return errors.New("missing persisted concurrency group")
		}
		eff := st.EffectiveCapacity
		if eff == 0 {
			eff = st.Capacity
		}
		qs.Resources = append(qs.Resources, wingwire.ResourceState{
			Key:      st.Key,
			Capacity: float64(eff),
			Held:     float64(st.UsedCost),
		})
		for _, h := range st.Holders {
			if h.Superseded {
				continue
			}
			qs.Holders = append(qs.Holders, wingwire.Holder{
				RunID:      runNode(h.RunID, h.NodeID),
				Semaphores: []string{st.Key},
				ElapsedMS:  sinceMS(h.ClaimedAt, now),
				Origin:     wingwire.OriginController,
			})
		}
		for _, wt := range st.Waiters {
			qs.Waiters = append(qs.Waiters, wingwire.Waiter{
				RunID:     runNode(wt.RunID, wt.NodeID),
				Position:  wt.Position + 1,
				WaitingOn: []string{st.Key},
				WaitingMS: sinceMS(wt.ArrivedAt, now),
				Origin:    wingwire.OriginController,
			})
		}
	}
	snapshot, err := s.runnerInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	for _, row := range snapshot.Runners {
		if h := row.Headroom; h != nil && snapshot.Now.Sub(h.ObservedAt) <= runnerHeadroomStale {
			qs.Runners = append(qs.Runners, wingwire.RunnerHeadroom{Name: row.Runner.Name, Cores: h.Cores, MemoryBytes: h.MemoryBytes, QueueDepth: h.QueueDepth})
		}
	}

	writeJSON(w, http.StatusOK, qs)
	return nil
}

func runNode(runID, nodeID string) string {
	if nodeID == "" {
		return runID
	}
	return runID + "/" + nodeID
}

func sinceMS(t, now time.Time) int64 {
	if t.IsZero() || !t.Before(now) {
		return 0
	}
	return now.Sub(t).Milliseconds()
}

var _ QueueReader = (*store.Tenant)(nil)
