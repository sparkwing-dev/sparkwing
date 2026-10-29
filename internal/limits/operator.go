package limits

import "time"

const (
	// idleClaimPoll supplies default maximum suggested idle poll.
	idleClaimPoll time.Duration = 5 * time.Second
	// LocalControllerHandshake supplies single local controller handshake budget.
	LocalControllerHandshake time.Duration = time.Minute
	// cloudEgressDailyAlarmBytes supplies funded profile deployment-wide egress alarm.
	cloudEgressDailyAlarmBytes int64 = 200 << 30
	// cloudFreeEgressDailyAlarmBytes supplies free profile deployment-wide egress alarm.
	cloudFreeEgressDailyAlarmBytes int64 = 20 << 30
	// runnersPerToken supplies self-named runners per root credential.
	runnersPerToken int = 64
	// shedQueueDepth leaves the team pending-queue guard unlimited by default.
	shedQueueDepth int = 0
	// requestsPerTokenMinute leaves the request guard unlimited by default.
	requestsPerTokenMinute int = 0
	// requestsPerMinuteAlarm disables the per-replica request alarm by default.
	requestsPerMinuteAlarm int = 0
	// bucketMeasurePages supplies maximum pages per bucket measurement.
	bucketMeasurePages int = 1000
	// cloudRequestsPerTokenMinute supplies funded profile token request budget per replica.
	cloudRequestsPerTokenMinute int = 3600
	// cloudFreeRequestsPerTokenMinute supplies free profile token request budget per replica.
	cloudFreeRequestsPerTokenMinute int = 600
	// cloudRequestRateAlarm supplies per-replica request-rate alarm.
	cloudRequestRateAlarm int = 5000
	// cloudRunsPerPrincipalHour supplies funded profile deployment-wide run-rate guard.
	cloudRunsPerPrincipalHour int = 600
	// cloudFreeRunsPerPrincipalHour supplies free profile deployment-wide run-rate guard.
	cloudFreeRunsPerPrincipalHour int = 60
	// cloudShedQueueDepth supplies funded profile team pending-queue guard.
	cloudShedQueueDepth int = 5000
	// cloudFreeShedQueueDepth supplies free profile team pending-queue guard.
	cloudFreeShedQueueDepth int = 1000
	// RunnerConcurrency supplies default configured runner concurrency.
	RunnerConcurrency int = 1
	// OperatorEnvMaskMinLength supplies minimum remote allow-listed value length registered for masking.
	OperatorEnvMaskMinLength int = 12
)
