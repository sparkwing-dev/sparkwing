package limits

import "time"

const (
	// DefaultLease bounds default work lease.
	DefaultLease time.Duration = 3 * time.Minute
	// MaxLease bounds maximum lease, bounded by claim expiry.
	MaxLease time.Duration = 10 * time.Minute
	// ClaimPoll bounds idle claim-loop poll.
	ClaimPoll time.Duration = 500 * time.Millisecond
	// PlacementHold bounds preferred placement hold.
	PlacementHold time.Duration = 20 * time.Second
	// PlacementLiveness bounds placement liveness horizon.
	PlacementLiveness time.Duration = 30 * time.Second
	// RankingWindow bounds how long a run-anywhere unit waits for its best-ranked claimant.
	RankingWindow time.Duration = 5 * time.Second
	// ClaimWindow bounds here credential must first claim within this window.
	ClaimWindow time.Duration = 10 * time.Minute
	// HeartbeatTimeout bounds shared heartbeat RPC timeout.
	HeartbeatTimeout time.Duration = 2 * time.Second
	// HeartbeatMaxSilence bounds shared heartbeat outage budget.
	HeartbeatMaxSilence time.Duration = 3 * time.Minute
	// MaxPollAdvice bounds maximum server poll advice.
	MaxPollAdvice time.Duration = 8 * time.Second
	// ChildAwait bounds child await request budget.
	ChildAwait time.Duration = 60 * time.Second
)
