package limits

import "time"

const (
	// AdmissionFrameBytes bounds admission socket request and reply frame.
	AdmissionFrameBytes int64 = 8 << 20
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
	// SecretJSONRequestBytes bounds secret controller JSON request envelope.
	SecretJSONRequestBytes int64 = 8 << 20
	// CloudEventRetention bounds default cloud event retention.
	CloudEventRetention time.Duration = 30 * 24 * time.Hour
	// DataUploadURLTTL bounds signed data upload URL lifetime.
	DataUploadURLTTL time.Duration = 15 * time.Minute
)
