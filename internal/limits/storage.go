package limits

import "time"

const (
	// UnpaidOutputBytes bounds individual small unpaid output.
	UnpaidOutputBytes int64 = 1 << 20
	// UnpaidOutputOverageBytes bounds unpaid small outputs beyond free allowance.
	UnpaidOutputOverageBytes int64 = 64 << 20
	// FreeStorageBytes bounds free storage allowance.
	FreeStorageBytes int64 = 1 << 30
	// FreeDailyDownloadBytes bounds free daily downloads.
	FreeDailyDownloadBytes int64 = 5 << 30
	// FundedDailyDownloadBytes bounds funded daily downloads.
	FundedDailyDownloadBytes int64 = 50 << 30
	// CacheArchiveBytes bounds dependency-cache archive.
	CacheArchiveBytes int64 = 500 << 20
	// BinaryBytes bounds pipeline binary.
	BinaryBytes int64 = 100 << 20
	// LocalBinaryCacheBytes bounds local binary-cache pruning budget.
	LocalBinaryCacheBytes int64 = 2 << 30
	// FreeTeamSlots bounds teams that may hold a free storage allowance.
	FreeTeamSlots int = 200
	// LocalBinaryCacheEntries bounds local binary-cache entries.
	LocalBinaryCacheEntries int = 20
	// MemoDefaultTTL bounds default memo TTL.
	MemoDefaultTTL time.Duration = 7 * 24 * time.Hour
	// MemoMaxTTL bounds maximum memo TTL; above-bound declarations are refused.
	MemoMaxTTL time.Duration = 30 * 24 * time.Hour
	// OutputRetention bounds output retention.
	OutputRetention time.Duration = 30 * 24 * time.Hour
	// FreeLogRetention bounds free log retention class.
	FreeLogRetention time.Duration = 30 * 24 * time.Hour
	// FundedLogRetention bounds funded log retention class.
	FundedLogRetention time.Duration = 90 * 24 * time.Hour
	// CloudRunRetention bounds cloud run retention; self-hosted default is disabled.
	CloudRunRetention time.Duration = 90 * 24 * time.Hour
	// BlobGCGrace bounds tombstone grace before unreachable blob collection.
	BlobGCGrace time.Duration = 24 * time.Hour
	// CapabilityRetention bounds retention after capability expiry or revocation.
	CapabilityRetention time.Duration = 7 * 24 * time.Hour
	// CompletionEffectRetention bounds retention after effect delivery.
	CompletionEffectRetention time.Duration = 7 * 24 * time.Hour
	// DataDownloadURLTTL bounds signed data download URL lifetime.
	DataDownloadURLTTL time.Duration = time.Minute
	// LogUploadURLTTL bounds batch log PUT URL lifetime.
	LogUploadURLTTL time.Duration = 15 * time.Minute
	// LogSegmentInterval bounds maximum segment buffering interval.
	LogSegmentInterval time.Duration = 10 * time.Second
	// WatchedLogPushInterval bounds watched live-log push cadence.
	WatchedLogPushInterval time.Duration = time.Second
	// LogSearchTimeout bounds run-scoped log search budget.
	LogSearchTimeout time.Duration = 10 * time.Second
	// LogSegmentBytes bounds log segment cut size.
	LogSegmentBytes int64 = 1 << 20
	// LogSearchBytes bounds run-scoped log search bytes.
	LogSearchBytes int64 = 256 << 20
	// ProtocolEnvelopeBytes bounds envelope allowance beyond a typed output payload.
	ProtocolEnvelopeBytes int64 = 1 << 20
	// LogUploadBatch bounds segment URLs per signing batch, plus manifest URL.
	LogUploadBatch int = 16
)
