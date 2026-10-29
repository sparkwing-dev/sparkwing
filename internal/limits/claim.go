package limits

import "time"

const (
	// DefaultLease bounds default work lease.
	DefaultLease time.Duration = 3 * time.Minute
	// ClaimPoll bounds idle claim-loop poll.
	ClaimPoll time.Duration = 500 * time.Millisecond
	// placementHold bounds preferred placement hold.
	placementHold time.Duration = 20 * time.Second
	// placementLiveness bounds placement liveness horizon.
	placementLiveness time.Duration = 30 * time.Second
	// RankingWindow bounds how long a ready job is held for a better-ranked available claimant.
	RankingWindow time.Duration = 5 * time.Second
	// RankingObservations caps the most recent live claimant observations one ranking check or notice evaluates.
	RankingObservations int = 64
	// ClaimWindow bounds here credential must first claim within this window.
	ClaimWindow time.Duration = 10 * time.Minute
	// HeartbeatTimeout bounds shared heartbeat RPC timeout.
	HeartbeatTimeout time.Duration = 2 * time.Second
	// ChildAwait bounds child await request budget.
	ChildAwait time.Duration = 60 * time.Second
)
