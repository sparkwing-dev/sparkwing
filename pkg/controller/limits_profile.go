package controller

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Limits profiles are the named sets of guard values a hosted controller runs
// with. An install that names none keeps the shipped defaults, where every
// budget below is zero and so unlimited.
const (
	// LimitsProfileCloud is what a paid hosted controller runs with.
	LimitsProfileCloud = "cloud"
	// LimitsProfileCloudFree is what a free-tier hosted controller runs with:
	// the same guards, sized for one runner rather than a pool.
	LimitsProfileCloudFree = "cloud-free"
)

// LimitsProfileValues are the guards one profile supplies. A profile fills a
// guard only where the operator named none, so an explicit flag always wins.
type LimitsProfileValues struct {
	// ClaimsPerRunnerMinute is [RequestBudget.ClaimsPerMinute].
	ClaimsPerRunnerMinute int
	// HeartbeatsPerRunnerMinute is [RequestBudget.HeartbeatsPerMinute].
	HeartbeatsPerRunnerMinute int
	// RequestsPerTokenMinute is [TokenRequestBudget.PerTokenMinute].
	RequestsPerTokenMinute int
	// RequestsPerMinuteAlarm is [TokenRequestBudget.AlarmPerMinute].
	RequestsPerMinuteAlarm int
	// MaxLogStreamsPerPrincipal is the concurrent live log streams one
	// principal may hold open.
	MaxLogStreamsPerPrincipal int
	// MaxDownloadsPerPrincipal is the concurrent metered downloads one
	// principal may hold open.
	MaxDownloadsPerPrincipal int
	// RunsPerPrincipalHour is [FloodPolicy.RunsPerPrincipalHour].
	RunsPerPrincipalHour int
	// ShedQueueDepth is [FloodPolicy.ShedQueueDepth].
	ShedQueueDepth int
	// EgressMonthlyBytesPerPrincipal is the bytes one principal may download
	// in a UTC month.
	EgressMonthlyBytesPerPrincipal int64
	// EgressDailyCapBytes is the bytes the controller may send in a UTC day
	// before it refuses every download, whoever asks.
	EgressDailyCapBytes int64
	// EnforceIdleClaimPoll refuses a claim that arrives sooner than the idle
	// interval the controller last suggested that runner. See
	// [Server.WithIdleClaimPollEnforced].
	EnforceIdleClaimPoll bool
}

// ClaimPollInterval is the cadence the shipped claim loops keep while the
// queue is empty. The hosted claim budgets are worked from it rather than from
// a round number, so an operator can check the arithmetic against the runner
// they actually run.
const ClaimPollInterval = 500 * time.Millisecond

// CompliantClaimPollsPerMinute reports the claim requests one runner makes in a
// minute of an empty queue while honoring its configured cadence. A claim
// budget below it refuses a runner that is doing exactly what it was asked to.
func CompliantClaimPollsPerMinute() int {
	return int(time.Minute / ClaimPollInterval)
}

// safety: past its empty-queue polling a loop spends one claim for each node it
// starts, so the paid tier allows three times the polling cadence in awards and
// the free tier one. A budget worked from a round number bounds nothing; one
// worked from the cadence alone refuses a runner that restarted mid-minute.
const (
	cloudClaimHeadroom     = 4
	cloudFreeClaimHeadroom = 2
)

// safety: a runner spends more than claims. A two-slot runner honoring its
// cadences spends roughly 300 requests a minute: 120 empty-queue claim polls,
// 40 node heartbeats at one every three seconds, and its state writes. The free
// tier carries one such runner and the paid tier several under one token.
const (
	cloudRequestsPerTokenMinute     = 2000
	cloudFreeRequestsPerTokenMinute = 600
)

// safety: the alarm is what one controller pod is sized to serve, so it sits
// above any single tenant's budget and speaks when several tenants at once, or
// a pod carrying more tenants than it was planned for, are driving it.
const cloudRequestRateAlarm = 5000

// safety: a principal's run cap is the guard a pending trigger nobody claims
// is written against, and the shed depth is the fleet's backstop behind it,
// sized so one controller's queue scan stays a fraction of a second.
const (
	cloudRunsPerPrincipalHour     = 600
	cloudFreeRunsPerPrincipalHour = 60
	cloudShedQueueDepth           = 5000
	cloudFreeShedQueueDepth       = 1000
)

// safety: egress is the bill a free account can run up without any compute, at
// about $0.09 a GiB. The daily cap bounds a controller's month at 31 times it
// however many accounts share it: $56 on the free tier and $560 on the paid.
const (
	cloudEgressMonthlyBytes      = 100 << 30
	cloudFreeEgressMonthlyBytes  = 5 << 30
	cloudEgressDailyCapBytes     = 200 << 30
	cloudFreeEgressDailyCapBytes = 20 << 30
)

// safety: the free tier halves the paid heartbeat budget and takes a fifth of
// its egress slots, which is the ratio the hosted tiers are priced on.
const freeHeartbeatDivisor = 2

var limitsProfiles = map[string]LimitsProfileValues{
	LimitsProfileCloud: {
		ClaimsPerRunnerMinute:     CompliantClaimPollsPerMinute() * cloudClaimHeadroom,
		HeartbeatsPerRunnerMinute: RecommendedHeartbeatsPerMinute,
		RequestsPerTokenMinute:    cloudRequestsPerTokenMinute,
		RequestsPerMinuteAlarm:    cloudRequestRateAlarm,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
		RunsPerPrincipalHour:      cloudRunsPerPrincipalHour,
		ShedQueueDepth:            cloudShedQueueDepth,

		EgressMonthlyBytesPerPrincipal: cloudEgressMonthlyBytes,
		EgressDailyCapBytes:            cloudEgressDailyCapBytes,
		EnforceIdleClaimPoll:           true,
	},
	LimitsProfileCloudFree: {
		ClaimsPerRunnerMinute:     CompliantClaimPollsPerMinute() * cloudFreeClaimHeadroom,
		HeartbeatsPerRunnerMinute: RecommendedHeartbeatsPerMinute / freeHeartbeatDivisor,
		RequestsPerTokenMinute:    cloudFreeRequestsPerTokenMinute,
		RequestsPerMinuteAlarm:    cloudRequestRateAlarm,
		MaxLogStreamsPerPrincipal: 10,
		MaxDownloadsPerPrincipal:  5,
		RunsPerPrincipalHour:      cloudFreeRunsPerPrincipalHour,
		ShedQueueDepth:            cloudFreeShedQueueDepth,

		EgressMonthlyBytesPerPrincipal: cloudFreeEgressMonthlyBytes,
		EgressDailyCapBytes:            cloudFreeEgressDailyCapBytes,
		EnforceIdleClaimPoll:           true,
	},
}

// LimitsProfile returns the guards name supplies. The empty name is the public
// default and supplies none, so an install that asks for no profile keeps
// every budget unlimited. An unknown name is an error rather than a silent
// default, because a typo would otherwise ship a hosted controller unguarded.
func LimitsProfile(name string) (LimitsProfileValues, error) {
	if name == "" {
		return LimitsProfileValues{}, nil
	}
	values, ok := limitsProfiles[name]
	if !ok {
		return LimitsProfileValues{}, fmt.Errorf(
			"unknown limits profile %q; want one of %s", name, strings.Join(LimitsProfileNames(), ", "))
	}
	return values, nil
}

// LimitsProfileNames returns every profile name, sorted.
func LimitsProfileNames() []string {
	out := make([]string, 0, len(limitsProfiles))
	for name := range limitsProfiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
