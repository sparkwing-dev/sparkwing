package limits

import "time"

const (
	// admissionFrameBytes bounds admission socket request and reply frame.
	admissionFrameBytes int64 = 8 << 20
	// LogSegmentUploadBytes bounds maximum encoded log segment accepted by storage.
	LogSegmentUploadBytes int64 = 8 << 20
	// LogManifestBytes bounds maximum log manifest.
	LogManifestBytes int64 = 4 << 20
	// MaxLogSegmentNumber bounds maximum log segment number.
	MaxLogSegmentNumber int = 1000000
	// storedAttemptLogBytes bounds stored attempt logs including compression headroom.
	storedAttemptLogBytes int64 = AttemptLogBytes + LogSegmentBytes
	// JSONRequestBytes bounds the ordinary controller JSON request envelope.
	JSONRequestBytes int64 = 1 << 20
	// secretJSONRequestBytes bounds secret controller JSON request envelope.
	secretJSONRequestBytes int64 = 8 << 20
	// cloudEventRetention bounds default cloud event retention.
	cloudEventRetention time.Duration = 30 * 24 * time.Hour
	// DataUploadURLTTL bounds signed data upload URL lifetime.
	DataUploadURLTTL time.Duration = 15 * time.Minute
)
