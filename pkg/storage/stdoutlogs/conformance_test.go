package stdoutlogs_test

import (
	"bytes"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/stdoutlogs"
)

func TestConformance_LogStore(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		return stdoutlogs.NewWithWriter(&bytes.Buffer{})
	})
}
