package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Placement reasons stamped on a node when a legacy claim wins it.
const (
	// PlacementPreferred marks a node taken by a runner that advertised its
	// preference.
	PlacementPreferred = "preference"
	// PlacementFallback marks a node taken by a runner that did not advertise
	// its preference, the hold window having passed.
	PlacementFallback = "fallback"
	// PlacementNone marks a node that carried no preference to honor.
	PlacementNone = "none"
)

// RunnerPresence is one legacy claim-mode runner a controller has heard from
// recently.
type RunnerPresence struct {
	Name string
	// Labels are the terms the runner asserted for itself on its last claim.
	// They steer the soft preference as sent; a hard selector reads them only
	// after [match.SelfAsserted] strips the keys a runner may not assert.
	Labels []string
	// Profile is what the controller knows of the runner apart from its labels:
	// the name its credential grants, its observed platform and capacity, and
	// the repositories it accepts.
	Profile match.Profile
	// FreeSlots is how many more nodes the runner said it can take. A runner
	// that advertised no capacity reports none, so it holds nothing back: an
	// unknown ceiling is not evidence of room.
	FreeSlots int
	// Available is the capacity the runner said it has free now, read only in
	// the dimensions its Profile reports a capacity for.
	Available match.Resources
	// TokenPrefix is the credential the runner last claimed with. The store
	// reads the runner's team off that credential's row, so a live runner of
	// another team never holds a node back; empty is the unauthenticated
	// local runner, which belongs to [DefaultTeam].
	TokenPrefix string
}

func (r RunnerPresence) profile() match.Profile {
	p := r.Profile
	p.Labels = match.SelfAsserted(r.Labels)
	return p
}

// safety: the hold and the offer window ask this one question, so they cannot
// disagree about which runner could take a node. A runner with no free slot
// still keeps the offer window, whose five seconds cover one poll.
func liveRunnerCould(r RunnerPresence, d match.Demand, needSlot bool) bool {
	if needSlot && (r.FreeSlots < 1 || !match.Fits(r.Profile.Capacity, r.Available, d.Request).OK()) {
		return false
	}
	return match.Evaluate(r.profile(), d).OK()
}

// ClaimPlacement is the local-first policy applied to one legacy claim. A node
// whose preference the claiming runner does not advertise waits out Hold,
// measured from the node's hold-from time, while another live runner both
// advertises that preference and satisfies the node's hard requirements with a
// slot to spare. After Hold any eligible runner takes it.
//
// The zero value holds nothing back, which is the behavior of every claim that
// carries no policy.
type ClaimPlacement struct {
	// DefaultPrefers applies to a node whose plan declares no Prefers.
	DefaultPrefers []string
	// Hold is how long a preferred-but-absent runner gets the node to itself.
	Hold time.Duration
	// Live are the other runners the controller has heard from inside its
	// liveness window.
	Live []RunnerPresence
}

type claimPlacementKey struct{}

// WithClaimPlacement carries a local-first policy into [Store.ClaimNextReadyNode]
// and its variants. A context without one claims first-in-first-out.
func WithClaimPlacement(ctx context.Context, placement ClaimPlacement) context.Context {
	return context.WithValue(ctx, claimPlacementKey{}, placement)
}

// ClaimPlacementFromContext returns the policy [WithClaimPlacement] carried.
func ClaimPlacementFromContext(ctx context.Context) (ClaimPlacement, bool) {
	placement, ok := ctx.Value(claimPlacementKey{}).(ClaimPlacement)
	return placement, ok
}

func (p ClaimPlacement) prefersFor(nodePrefers []string) []string {
	if len(nodePrefers) > 0 {
		return nodePrefers
	}
	return p.DefaultPrefers
}

type placementDecision struct {
	reason string
	hold   bool
	// safety: a preference the plan declared is worth recording per node; one
	// the controller supplies for every node is not.
	nodeOwned bool
}

func (p ClaimPlacement) decide(demand match.Demand, nodePrefers, runnerLabels []string, holdFrom *time.Time, now time.Time) placementDecision {
	prefers := p.prefersFor(nodePrefers)
	nodeOwned := len(nodePrefers) > 0
	switch {
	case len(prefers) == 0:
		return placementDecision{reason: PlacementNone}
	case preferenceMet(prefers, runnerLabels):
		return placementDecision{reason: PlacementPreferred, nodeOwned: nodeOwned}
	case p.Hold > 0 && holdFrom != nil && now.Before(holdFrom.Add(p.Hold)) && p.someLiveRunnerCanTakeIt(demand, prefers):
		return placementDecision{hold: true, nodeOwned: nodeOwned}
	default:
		return placementDecision{reason: PlacementFallback, nodeOwned: nodeOwned}
	}
}

// safety: another team's runner can never claim this node, so it earns no
// hold. Its team comes off its token row, never off what it asserted, and a
// runner whose team cannot be established holds nothing back.
func (s *Store) placementForTeam(ctx context.Context, p ClaimPlacement, team Team) (ClaimPlacement, error) {
	if p.Hold <= 0 || len(p.Live) == 0 {
		return p, nil
	}
	teams, err := s.runnerTeams(ctx, p.Live)
	if err != nil {
		return ClaimPlacement{}, err
	}
	live := make([]RunnerPresence, 0, len(p.Live))
	var sole *Team
	for _, runner := range p.Live {
		owner := teams[runner.TokenPrefix]
		if owner == "" && sole == nil {
			// safety: a prefix no token row backs belongs to the sole team of a
			// single-team install, exactly as [Store.claimScope] places it.
			scope, err := s.soleTeamScope(ctx)
			if err != nil && !errors.Is(err, ErrClaimantHasNoTeam) {
				return ClaimPlacement{}, err
			}
			sole = &scope.team
		}
		if owner == "" {
			owner = *sole
		}
		if owner != "" && owner == team {
			live = append(live, runner)
		}
	}
	p.Live = live
	return p, nil
}

// safety: an unauthenticated runner is the local path, which the tenant
// migration put in [DefaultTeam]; a prefix no row backs maps to no team.
func (s *Store) runnerTeams(ctx context.Context, live []RunnerPresence) (_ map[string]Team, err error) {
	teams := map[string]Team{"": DefaultTeam}
	var prefixes []any
	for _, runner := range live {
		if _, seen := teams[runner.TokenPrefix]; !seen {
			teams[runner.TokenPrefix] = ""
			prefixes = append(prefixes, runner.TokenPrefix)
		}
	}
	if len(prefixes) == 0 {
		return teams, nil
	}
	rows, err := s.query(ctx, `SELECT prefix, team FROM tokens WHERE prefix IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(prefixes)), ",")+`)`, prefixes...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var prefix, owner string
		if err := rows.Scan(&prefix, &owner); err != nil {
			return nil, err
		}
		teams[prefix] = NormalizeTeam(Team(owner))
	}
	return teams, rows.Err()
}

// safety: a runner that cannot execute the node is no reason to withhold it
// from one that can, so the preference alone never earns a hold.
func (p ClaimPlacement) someLiveRunnerCanTakeIt(demand match.Demand, prefers []string) bool {
	for _, runner := range p.Live {
		if liveRunnerCould(runner, demand, true) && preferenceMet(prefers, runner.Labels) {
			return true
		}
	}
	return false
}

// safety: one satisfied term is the whole preference, as in the enrolled offer
// round where the first matching term ranks an executor. A preference reads the
// labels as asserted, because a runner's own location speaks to it.
func preferenceMet(prefers, labels []string) bool {
	asserted := match.Profile{Labels: labels}
	for _, term := range prefers {
		if term != "" && match.TermSatisfied(asserted, term) {
			return true
		}
	}
	return false
}

// safety: the node's repository costs a read, so it is looked up only when some
// live runner's list could turn on it.
func (p ClaimPlacement) needsRunRepository() bool {
	if p.Hold <= 0 {
		return false
	}
	for _, runner := range p.Live {
		if runner.Profile.Accept != nil {
			return true
		}
	}
	return false
}

type nodeKey struct {
	runID  string
	nodeID string
}

type nodePlacementEvent struct {
	HolderID string   `json:"holder_id"`
	Reason   string   `json:"reason"`
	Prefers  []string `json:"prefers,omitempty"`
}

type queueRunnersKey struct{}

// WithQueueRunners carries every legacy claim-mode runner a controller has
// heard from inside its liveness window into [Store.MarkNodeReady], which may
// then open a round already due when none of them could claim the node. The
// controller attaches it only once its registry has listened for a whole
// window; a context without it leaves every offer round its window.
func WithQueueRunners(ctx context.Context, live []RunnerPresence) context.Context {
	return context.WithValue(ctx, queueRunnersKey{}, live)
}
