package limits

import "time"

const (
	// LogSegmentUploadBytes bounds maximum encoded log segment accepted by storage.
	LogSegmentUploadBytes int64 = 8 << 20
	// LogManifestBytes bounds maximum log manifest.
	LogManifestBytes int64 = 4 << 20
	// MaxLogSegmentNumber bounds maximum log segment number.
	MaxLogSegmentNumber int = 1000000
	// JSONRequestBytes bounds the ordinary controller JSON request envelope.
	JSONRequestBytes int64 = 1 << 20
	// DataUploadURLTTL bounds signed data upload URL lifetime.
	DataUploadURLTTL time.Duration = 15 * time.Minute
)
