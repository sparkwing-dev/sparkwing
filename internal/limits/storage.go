package limits

import (
	"fmt"
	"time"
)

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
	// PreparedPipelineBytes bounds the prepared pipeline archive Sparkwing Cloud
	// builds and publishes for a run. The archive does not count toward the
	// team's storage quota, so this cap is what bounds it.
	PreparedPipelineBytes int64 = 256 << 20
	// ProtocolEnvelopeBytes bounds envelope allowance beyond a typed output payload.
	ProtocolEnvelopeBytes int64 = 1 << 20
)

// PreparedPipelineTooLarge names the prepared pipeline limit an archive of
// size bytes exceeds and how to get under it.
func PreparedPipelineTooLarge(size int64) string {
	return fmt.Sprintf("prepared pipeline is %d bytes, over the %d-byte (256 MiB) limit for prepared pipelines; "+
		"shrink the pipeline module, for example by trimming its dependencies, and keep large generated or vendored files out of the repository checkout",
		size, PreparedPipelineBytes)
}
