package limits

import "time"

const (
	// OIDCDefaultTTL supplies default provider OIDC lifetime.
	OIDCDefaultTTL time.Duration = 10 * time.Minute
	// OIDCMinTTL supplies minimum provider OIDC lifetime.
	OIDCMinTTL time.Duration = time.Minute
	// OIDCMaxTTL supplies maximum provider OIDC lifetime.
	OIDCMaxTTL time.Duration = time.Hour
	// LiveLogIdleTimeout supplies idle ring eviction delay per replica; durable segments remain readable.
	LiveLogIdleTimeout time.Duration = 10 * time.Minute
	// IdleClaimPoll supplies default maximum suggested idle poll.
	IdleClaimPoll time.Duration = 5 * time.Second
	// BucketReconcileInterval supplies object bucket measurement interval.
	BucketReconcileInterval time.Duration = time.Hour
	// RunnerIdleExit disables runner idle exit by default.
	RunnerIdleExit time.Duration = 0
	// RunnerHeartbeat supplies default renewal cadence derived from the default lease.
	RunnerHeartbeat time.Duration = DefaultLease / 3
	// ProcessTerminationGrace supplies process-group graceful termination budget.
	ProcessTerminationGrace time.Duration = time.Second
	// LocalControllerDrain supplies local controller finalization drain.
	LocalControllerDrain time.Duration = 10 * time.Second
	// LocalControllerTermGrace supplies supervisor termination grace.
	LocalControllerTermGrace time.Duration = 15 * time.Second
	// LocalControllerReattach supplies client recovery budget across controller replacements.
	LocalControllerReattach time.Duration = 12 * time.Minute
	// LocalControllerHandshake supplies single local controller handshake budget.
	LocalControllerHandshake time.Duration = time.Minute
	// LocalControllerRecoveryGrace supplies restored lease grace after controller startup.
	LocalControllerRecoveryGrace time.Duration = 2 * time.Minute
	// LiveLogJobBytes supplies per-job live ring per replica.
	LiveLogJobBytes int64 = 512 << 10
	// LiveLogTotalBytes supplies total live rings per replica.
	LiveLogTotalBytes int64 = 64 << 20
	// BucketMaxBytes disables the deployment bucket byte ceiling by default.
	BucketMaxBytes int64 = 0
	// BucketWarnBytes disables the deployment bucket byte warning by default.
	BucketWarnBytes int64 = 0
	// EgressDailyAlarmBytes disables the deployment-wide egress alarm by default.
	EgressDailyAlarmBytes int64 = 0
	// CloudEgressDailyAlarmBytes supplies funded profile deployment-wide egress alarm.
	CloudEgressDailyAlarmBytes int64 = 200 << 30
	// CloudFreeEgressDailyAlarmBytes supplies free profile deployment-wide egress alarm.
	CloudFreeEgressDailyAlarmBytes int64 = 20 << 30
	// LiveLogMaxJobs supplies concurrent live rings per replica.
	LiveLogMaxJobs int = 1024
	// RunnersPerToken supplies self-named runners per root credential.
	RunnersPerToken int = 64
	// RunsPerPrincipalHour leaves the deployment-wide run-rate guard unlimited by default.
	RunsPerPrincipalHour int = 0
	// ShedQueueDepth leaves the team pending-queue guard unlimited by default.
	ShedQueueDepth int = 0
	// ClaimsPerRunnerMinute leaves the claim request guard unlimited by default.
	ClaimsPerRunnerMinute int = 0
	// HeartbeatsPerRunnerMinute leaves the heartbeat request guard unlimited by default.
	HeartbeatsPerRunnerMinute int = 0
	// RequestsPerTokenMinute leaves the request guard unlimited by default.
	RequestsPerTokenMinute int = 0
	// RequestsPerMinuteAlarm disables the per-replica request alarm by default.
	RequestsPerMinuteAlarm int = 0
	// MaxLogStreamsPerPrincipal leaves the stream concurrency guard unlimited by default.
	MaxLogStreamsPerPrincipal int = 0
	// MaxDownloadsPerPrincipal leaves the download concurrency guard unlimited by default.
	MaxDownloadsPerPrincipal int = 0
	// BucketMaxObjects disables the deployment object ceiling by default.
	BucketMaxObjects int = 0
	// BucketWarnObjects disables the deployment object warning by default.
	BucketWarnObjects int = 0
	// BucketMeasurePages supplies maximum pages per bucket measurement.
	BucketMeasurePages int = 1000
	// CloudClaimsPerRunnerMinute supplies funded profile claim request budget per replica.
	CloudClaimsPerRunnerMinute int = int(time.Minute/ClaimPoll) * 4
	// CloudFreeClaimsPerRunnerMinute supplies free profile claim request budget per replica.
	CloudFreeClaimsPerRunnerMinute int = int(time.Minute/ClaimPoll) * 2
	// CloudHeartbeatsPerRunnerMinute supplies funded profile heartbeat request budget per replica.
	CloudHeartbeatsPerRunnerMinute int = 1200
	// CloudFreeHeartbeatsPerRunnerMinute supplies free profile heartbeat request budget per replica.
	CloudFreeHeartbeatsPerRunnerMinute int = CloudHeartbeatsPerRunnerMinute / 2
	// CloudRequestsPerTokenMinute supplies funded profile token request budget per replica.
	CloudRequestsPerTokenMinute int = 3600
	// CloudFreeRequestsPerTokenMinute supplies free profile token request budget per replica.
	CloudFreeRequestsPerTokenMinute int = 600
	// CloudRequestRateAlarm supplies per-replica request-rate alarm.
	CloudRequestRateAlarm int = 5000
	// CloudRunsPerPrincipalHour supplies funded profile deployment-wide run-rate guard.
	CloudRunsPerPrincipalHour int = 600
	// CloudFreeRunsPerPrincipalHour supplies free profile deployment-wide run-rate guard.
	CloudFreeRunsPerPrincipalHour int = 60
	// CloudShedQueueDepth supplies funded profile team pending-queue guard.
	CloudShedQueueDepth int = 5000
	// CloudFreeShedQueueDepth supplies free profile team pending-queue guard.
	CloudFreeShedQueueDepth int = 1000
	// CloudMaxLogStreams supplies funded profile live streams per principal per replica.
	CloudMaxLogStreams int = 50
	// CloudFreeMaxLogStreams supplies free profile live streams per principal per replica.
	CloudFreeMaxLogStreams int = 10
	// CloudMaxDownloads supplies funded profile downloads per principal per replica.
	CloudMaxDownloads int = 20
	// CloudFreeMaxDownloads supplies free profile downloads per principal per replica.
	CloudFreeMaxDownloads int = 5
	// ObjectPutPerMinute supplies per-replica object PUT request budget.
	ObjectPutPerMinute int = 1200
	// ObjectPutPerDay supplies per-replica object PUT daily budget.
	ObjectPutPerDay int = 200000
	// ObjectGetPerMinute supplies per-replica object GET request budget.
	ObjectGetPerMinute int = 3000
	// ObjectGetPerDay supplies per-replica object GET daily budget.
	ObjectGetPerDay int = 500000
	// ObjectListPerMinute supplies per-replica object LIST request budget.
	ObjectListPerMinute int = 600
	// ObjectListPerDay supplies per-replica object LIST daily budget.
	ObjectListPerDay int = 100000
	// ObjectDeletePerMinute supplies per-replica object DELETE request budget.
	ObjectDeletePerMinute int = 600
	// ObjectDeletePerDay supplies per-replica object DELETE daily budget.
	ObjectDeletePerDay int = 100000
	// RunnerConcurrency supplies default configured runner concurrency.
	RunnerConcurrency int = 1
	// TeamCloudJobs supplies default team cloud compute concurrency.
	TeamCloudJobs int = 100
	// MaxTeamCloudJobs supplies maximum configured team cloud compute concurrency.
	MaxTeamCloudJobs int = 10000
	// OperatorEnvMaskMinLength supplies minimum remote allow-listed value length registered for masking.
	OperatorEnvMaskMinLength int = 12
)
