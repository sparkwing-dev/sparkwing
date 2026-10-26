package limits

import "time"

const (
	// unpaidOutputBytes bounds individual small unpaid output.
	unpaidOutputBytes int64 = 1 << 20
	// unpaidOutputOverageBytes bounds unpaid small outputs beyond free allowance.
	unpaidOutputOverageBytes int64 = 64 << 20
	// freeStorageBytes bounds free storage allowance.
	freeStorageBytes int64 = 1 << 30
	// freeDailyDownloadBytes bounds free daily downloads.
	freeDailyDownloadBytes int64 = 5 << 30
	// fundedDailyDownloadBytes bounds funded daily downloads.
	fundedDailyDownloadBytes int64 = 50 << 30
	// CacheArchiveBytes bounds dependency-cache archive.
	CacheArchiveBytes int64 = 500 << 20
	// binaryBytes bounds pipeline binary.
	binaryBytes int64 = 100 << 20
	// LocalBinaryCacheBytes bounds local binary-cache pruning budget.
	LocalBinaryCacheBytes int64 = 2 << 30
	// LocalBinaryCacheEntries bounds local binary-cache entries.
	LocalBinaryCacheEntries int = 20
	// MemoDefaultTTL bounds default memo TTL.
	MemoDefaultTTL time.Duration = 7 * 24 * time.Hour
	// MemoMaxTTL bounds maximum memo TTL; above-bound declarations are refused.
	MemoMaxTTL time.Duration = 30 * 24 * time.Hour
	// outputRetention bounds output retention.
	outputRetention time.Duration = 30 * 24 * time.Hour
	// FreeLogRetention bounds free log retention class.
	FreeLogRetention time.Duration = 30 * 24 * time.Hour
	// FundedLogRetention bounds funded log retention class.
	FundedLogRetention time.Duration = 90 * 24 * time.Hour
	// BlobGCGrace bounds tombstone grace before unreachable blob collection.
	BlobGCGrace time.Duration = 24 * time.Hour
	// capabilityRetention bounds retention after capability expiry or revocation.
	capabilityRetention time.Duration = 7 * 24 * time.Hour
	// completionEffectRetention bounds retention after effect delivery.
	completionEffectRetention time.Duration = 7 * 24 * time.Hour
	// DataDownloadURLTTL bounds signed data download URL lifetime.
	DataDownloadURLTTL time.Duration = time.Minute
	// LogUploadURLTTL bounds batch log PUT URL lifetime.
	LogUploadURLTTL time.Duration = 15 * time.Minute
	// logSegmentInterval bounds maximum segment buffering interval.
	logSegmentInterval time.Duration = 10 * time.Second
	// watchedLogPushInterval bounds watched live-log push cadence.
	watchedLogPushInterval time.Duration = time.Second
	// LogSearchTimeout bounds run-scoped log search budget.
	LogSearchTimeout time.Duration = 10 * time.Second
	// LogSegmentBytes bounds log segment cut size.
	LogSegmentBytes int64 = 1 << 20
	// ProjectConfigBytes bounds one sparkwing.yaml.
	ProjectConfigBytes int64 = 1 << 20
	// LogSearchBytes bounds run-scoped log search bytes.
	LogSearchBytes int64 = 256 << 20
	// ProtocolEnvelopeBytes bounds envelope allowance beyond a typed output payload.
	ProtocolEnvelopeBytes int64 = 1 << 20
)
