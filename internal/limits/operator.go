package limits

import "time"

const (
	// oidcDefaultTTL supplies default provider OIDC lifetime.
	oidcDefaultTTL time.Duration = 10 * time.Minute
	// oidcMinTTL supplies minimum provider OIDC lifetime.
	oidcMinTTL time.Duration = time.Minute
	// oidcMaxTTL supplies maximum provider OIDC lifetime.
	oidcMaxTTL time.Duration = time.Hour
	// liveLogIdleTimeout supplies idle ring eviction delay per replica; durable segments remain readable.
	liveLogIdleTimeout time.Duration = 10 * time.Minute
	// idleClaimPoll supplies default maximum suggested idle poll.
	idleClaimPoll time.Duration = 5 * time.Second
	// bucketReconcileInterval supplies object bucket measurement interval.
	bucketReconcileInterval time.Duration = time.Hour
	// runnerIdleExit disables runner idle exit by default.
	runnerIdleExit time.Duration = 0
	// runnerHeartbeat supplies default renewal cadence derived from the default lease.
	runnerHeartbeat time.Duration = DefaultLease / 3
	// processTerminationGrace supplies process-group graceful termination budget.
	processTerminationGrace time.Duration = time.Second
	// localControllerDrain supplies local controller finalization drain.
	localControllerDrain time.Duration = 10 * time.Second
	// localControllerTermGrace supplies supervisor termination grace.
	localControllerTermGrace time.Duration = 15 * time.Second
	// localControllerReattach supplies client recovery budget across controller replacements.
	localControllerReattach time.Duration = 12 * time.Minute
	// LocalControllerHandshake supplies single local controller handshake budget.
	LocalControllerHandshake time.Duration = time.Minute
	// localControllerRecoveryGrace supplies restored lease grace after controller startup.
	localControllerRecoveryGrace time.Duration = 2 * time.Minute
	// liveLogJobBytes supplies per-job live ring per replica.
	liveLogJobBytes int64 = 512 << 10
	// liveLogTotalBytes supplies total live rings per replica.
	liveLogTotalBytes int64 = 64 << 20
	// bucketMaxBytes disables the deployment bucket byte ceiling by default.
	bucketMaxBytes int64 = 0
	// bucketWarnBytes disables the deployment bucket byte warning by default.
	bucketWarnBytes int64 = 0
	// egressDailyAlarmBytes disables the deployment-wide egress alarm by default.
	egressDailyAlarmBytes int64 = 0
	// cloudEgressDailyAlarmBytes supplies funded profile deployment-wide egress alarm.
	cloudEgressDailyAlarmBytes int64 = 200 << 30
	// cloudFreeEgressDailyAlarmBytes supplies free profile deployment-wide egress alarm.
	cloudFreeEgressDailyAlarmBytes int64 = 20 << 30
	// liveLogMaxJobs supplies concurrent live rings per replica.
	liveLogMaxJobs int = 1024
	// runnersPerToken supplies self-named runners per root credential.
	runnersPerToken int = 64
	// runsPerPrincipalHour leaves the deployment-wide run-rate guard unlimited by default.
	runsPerPrincipalHour int = 0
	// shedQueueDepth leaves the team pending-queue guard unlimited by default.
	shedQueueDepth int = 0
	// claimsPerRunnerMinute leaves the claim request guard unlimited by default.
	claimsPerRunnerMinute int = 0
	// heartbeatsPerRunnerMinute leaves the heartbeat request guard unlimited by default.
	heartbeatsPerRunnerMinute int = 0
	// requestsPerTokenMinute leaves the request guard unlimited by default.
	requestsPerTokenMinute int = 0
	// requestsPerMinuteAlarm disables the per-replica request alarm by default.
	requestsPerMinuteAlarm int = 0
	// maxLogStreamsPerPrincipal leaves the stream concurrency guard unlimited by default.
	maxLogStreamsPerPrincipal int = 0
	// maxDownloadsPerPrincipal leaves the download concurrency guard unlimited by default.
	maxDownloadsPerPrincipal int = 0
	// bucketMaxObjects disables the deployment object ceiling by default.
	bucketMaxObjects int = 0
	// bucketWarnObjects disables the deployment object warning by default.
	bucketWarnObjects int = 0
	// bucketMeasurePages supplies maximum pages per bucket measurement.
	bucketMeasurePages int = 1000
	// cloudClaimsPerRunnerMinute supplies funded profile claim request budget per replica.
	cloudClaimsPerRunnerMinute int = int(time.Minute/ClaimPoll) * 4
	// cloudFreeClaimsPerRunnerMinute supplies free profile claim request budget per replica.
	cloudFreeClaimsPerRunnerMinute int = int(time.Minute/ClaimPoll) * 2
	// cloudHeartbeatsPerRunnerMinute supplies funded profile heartbeat request budget per replica.
	cloudHeartbeatsPerRunnerMinute int = 1200
	// cloudFreeHeartbeatsPerRunnerMinute supplies free profile heartbeat request budget per replica.
	cloudFreeHeartbeatsPerRunnerMinute int = cloudHeartbeatsPerRunnerMinute / 2
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
	// cloudMaxLogStreams supplies funded profile live streams per principal per replica.
	cloudMaxLogStreams int = 50
	// cloudFreeMaxLogStreams supplies free profile live streams per principal per replica.
	cloudFreeMaxLogStreams int = 10
	// cloudMaxDownloads supplies funded profile downloads per principal per replica.
	cloudMaxDownloads int = 20
	// cloudFreeMaxDownloads supplies free profile downloads per principal per replica.
	cloudFreeMaxDownloads int = 5
	// objectPutPerMinute supplies per-replica object PUT request budget.
	objectPutPerMinute int = 1200
	// objectPutPerDay supplies per-replica object PUT daily budget.
	objectPutPerDay int = 200000
	// objectGetPerMinute supplies per-replica object GET request budget.
	objectGetPerMinute int = 3000
	// objectGetPerDay supplies per-replica object GET daily budget.
	objectGetPerDay int = 500000
	// objectListPerMinute supplies per-replica object LIST request budget.
	objectListPerMinute int = 600
	// objectListPerDay supplies per-replica object LIST daily budget.
	objectListPerDay int = 100000
	// objectDeletePerMinute supplies per-replica object DELETE request budget.
	objectDeletePerMinute int = 600
	// objectDeletePerDay supplies per-replica object DELETE daily budget.
	objectDeletePerDay int = 100000
	// RunnerConcurrency supplies default configured runner concurrency.
	RunnerConcurrency int = 1
	// teamCloudJobs supplies default team cloud compute concurrency.
	teamCloudJobs int = 100
	// maxTeamCloudJobs supplies maximum configured team cloud compute concurrency.
	maxTeamCloudJobs int = 10000
	// OperatorEnvMaskMinLength supplies minimum remote allow-listed value length registered for masking.
	OperatorEnvMaskMinLength int = 12
)
