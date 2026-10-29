package limits

import "time"

const (
	// CacheArchiveBytes bounds dependency-cache archive.
	CacheArchiveBytes int64 = 500 << 20
	// LocalBinaryCacheBytes bounds local binary-cache pruning budget.
	LocalBinaryCacheBytes int64 = 2 << 30
	// LocalBinaryCacheEntries bounds local binary-cache entries.
	LocalBinaryCacheEntries int = 20
	// MemoDefaultTTL bounds default memo TTL.
	MemoDefaultTTL time.Duration = 7 * 24 * time.Hour
	// MemoMaxTTL bounds maximum memo TTL; above-bound declarations are refused.
	MemoMaxTTL time.Duration = 30 * 24 * time.Hour
	// FreeLogRetention bounds free log retention class.
	FreeLogRetention time.Duration = 30 * 24 * time.Hour
	// FundedLogRetention bounds funded log retention class.
	FundedLogRetention time.Duration = 90 * 24 * time.Hour
	// BlobGCGrace bounds tombstone grace before unreachable blob collection.
	BlobGCGrace time.Duration = 24 * time.Hour
	// DataDownloadURLTTL bounds signed data download URL lifetime.
	DataDownloadURLTTL time.Duration = time.Minute
	// LogUploadURLTTL bounds batch log PUT URL lifetime.
	LogUploadURLTTL time.Duration = 15 * time.Minute
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
