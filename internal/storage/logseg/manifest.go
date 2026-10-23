package logseg

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// manifestVersion is the manifest format this package writes and reads.
const manifestVersion = 1

// Manifest statuses describe the writer's final payload.
const (
	StatusComplete   = "complete"
	statusIncomplete = "incomplete"
	statusCutOff     = "cut_off"
)

// Reasons a manifest gives for any status but complete.
const (
	reasonUploadFailed = "upload failed"
	reasonByteCap      = "byte cap"
	reasonSequenceGap  = "sequence gap"
	reasonDropped      = "writer dropped lines"
	reasonUnsealed     = "writer did not seal"
	reasonStopped      = "node stopped before its log was finished"
)

// sealedByNode identifies the writer of a physical manifest.
const sealedByNode = "node"

// Manifest describes one subject's final segments; persisted records and seals govern reads.
type Manifest struct {
	Subject      *blob.LogSubject `json:"subject"`
	Version      int              `json:"version"`
	Status       string           `json:"status"`
	Reason       string           `json:"reason"`
	SealedBy     string           `json:"sealed_by"`
	SealedAt     time.Time        `json:"sealed_at"`
	FirstSeq     int64            `json:"first_seq"`
	LastSeq      int64            `json:"last_seq"`
	Lines        int64            `json:"lines"`
	Bytes        int64            `json:"bytes"`
	DroppedLines int64            `json:"dropped_lines"`
	Gaps         []Gap            `json:"gaps"`
	Segments     []Segment        `json:"segments"`
}

// Segment is one uploaded segment object. Line numbers count the lines
// the sink stored, from 1, so FirstSeq and Lines locate a segment for a
// head or tail read without fetching the ones before it.
type Segment struct {
	N           int    `json:"n"`
	FirstSeq    int64  `json:"first_seq"`
	LastSeq     int64  `json:"last_seq"`
	Lines       int64  `json:"lines"`
	Bytes       int64  `json:"bytes"`
	StoredBytes int64  `json:"stored_bytes"`
	SHA256      string `json:"sha256"`
}

// Gap is an inclusive range of the writer's own sequence numbers that
// never reached the sink.
type Gap struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Marshal encodes m for upload.
func (m Manifest) Marshal() ([]byte, error) {
	if m.Subject == nil {
		return nil, errBadManifest
	}
	if err := m.Subject.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadManifest, err)
	}
	if m.Gaps == nil {
		m.Gaps = []Gap{}
	}
	if m.Segments == nil {
		m.Segments = []Segment{}
	}
	return json.Marshal(m)
}

// errBadManifest marks a manifest object that does not decode, or that
// names a version or status this package does not know.
var errBadManifest = errors.New("logseg: unreadable manifest")

// parseManifest decodes a manifest object and checks its shape.
func parseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadManifest, err)
	}
	if m.Version != manifestVersion {
		return nil, fmt.Errorf("%w: version %d", errBadManifest, m.Version)
	}
	if m.Subject == nil {
		return nil, errBadManifest
	}
	if err := m.Subject.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadManifest, err)
	}
	switch m.Status {
	case StatusComplete, statusIncomplete, statusCutOff:
	default:
		return nil, fmt.Errorf("%w: status %q", errBadManifest, m.Status)
	}
	for i, seg := range m.Segments {
		if seg.N != i+1 {
			return nil, fmt.Errorf("%w: segment %d listed at position %d", errBadManifest, seg.N, i+1)
		}
	}
	return &m, nil
}

// Completeness states a reader reports for one node's log.
const (
	// StateStreaming identifies a log that can still receive bytes.
	StateStreaming = "streaming"
	// StateComplete identifies a persisted complete seal with verified segment bytes.
	StateComplete = "complete"
	// StateIncomplete identifies a persisted seal reporting missing lines; unreadable bytes fail reads.
	StateIncomplete = "incomplete"
	// StateCutOff: the log stopped before its writer finished: the node
	// was stopped, or the byte cap ended it.
	stateCutOff = "cut_off"
	// StateUnknown: the log store does not track completeness.
	StateUnknown = "unknown"
)

// Completeness is a reader's verdict on one node's log. Message is the
// synthetic line a reader renders after the log; it is empty when there
// is nothing to say.
type Completeness struct {
	State        string `json:"state"`
	Lines        int64  `json:"lines"`
	MissingLines int64  `json:"missing_lines,omitempty"`
	Message      string `json:"message,omitempty"`
}
