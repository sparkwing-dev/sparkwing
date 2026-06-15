package stdoutlogs_test

import (
	"bytes"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/stdoutlogs"
)

// TestConformance_LogStore wires the LogStore conformance suite
// against the write-only stdoutlogs backend. Every read path
// (Read, ReadRun, Stream) returns
// [stdoutlogs.ErrReadUnsupported] which wraps
// [storage.ErrNotSupported], so the suite's read-side subtests
// register as skipped. Append + DeleteRun (no-op) are exercised.
func TestConformance_LogStore(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		return stdoutlogs.NewWithWriter(&bytes.Buffer{})
	})
}
