package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// QueueReader supplies the concurrency groups visible to one authorized team.
// Its methods preserve Tenant's read contract without exposing deployment-wide reads.
type QueueReader interface {
	Team() store.Team
	ListConcurrencyStates(context.Context) ([]*store.ConcurrencyState, error)
}

func (s *Server) queueReader(ctx context.Context, team string) (QueueReader, error) {
	return tenantPort[QueueReader](ctx, s, store.Team(team))
}

// LocalAdmission reads the admission daemon on the local controller's own machine. It answers an empty state with
// no error when no daemon is running.
type LocalAdmission func(context.Context) (wingwire.QueueState, error)

// WithLocalAdmission makes GET /api/v1/queue report the machine's admission daemon (its version, host capacity,
// holders and waiters) beside the controller's concurrency groups. Only the local controller shares a machine with
// one, so a team or Cloud controller leaves it unset.
func (s *Server) WithLocalAdmission(read LocalAdmission) *Server {
	s.localAdmission = read
	return s
}

func (s *Server) queueEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
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
	qs := wingwire.QueueState{}
	if s.localAdmission != nil {
		if qs, err = s.localAdmission(r.Context()); err != nil {
			// safety: the policy writer replaces a 5xx message with "request failed", so this fixed public message is
			// written here and the cause goes only to the request log. Daemon status, stop and restart speak over the
			// socket that went quiet, so stopping this process by hand is the one recovery that works.
			apierr.NoteResponseError(w, err)
			pid := strconv.Itoa(os.Getpid())
			return apierr.WriteHTTP(w, &apierr.Error{
				Code: apierr.Unavailable, Message: "The local admission daemon did not answer.",
				Hint: "Stop the sparkwing process " + pid + " by hand (for example `kill " + pid + "`), then run `sparkwing dashboard start`.",
			})
		}
	}
	now := time.Now()
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
				RunID: h.RunID, ParticipantID: runNode(h.RunID, h.NodeID), DisplayRunID: runNode(h.RunID, h.NodeID),
				Semaphores: []string{st.Key},
				ElapsedMS:  sinceMS(h.ClaimedAt, now),
				Origin:     wingwire.OriginController,
			})
		}
		for _, wt := range st.Waiters {
			qs.Waiters = append(qs.Waiters, wingwire.Waiter{
				RunID: wt.RunID, ParticipantID: runNode(wt.RunID, wt.NodeID), DisplayRunID: runNode(wt.RunID, wt.NodeID),
				Position:       wt.Position + 1,
				WaitingOn:      []string{st.Key},
				BlockingReason: wt.BlockingReason,
				WaitingMS:      sinceMS(wt.ArrivedAt, now),
				Origin:         wingwire.OriginController,
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
