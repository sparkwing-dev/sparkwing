package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestEngineNoticePrintsOncePerProcessStream(t *testing.T) {
	shownEngineNotices.Clear()
	t.Cleanup(shownEngineNotices.Clear)
	notices := []engineid.Notice{{Kind: engineid.NoticePolicyUnavailable, Reason: "No verified version policy is cached.", Hint: "Run `sparkwing update`."}}
	stream, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for range 3 {
		if err := writeEngineNoticesOnce(stream, notices); err != nil {
			t.Fatal(err)
		}
	}
	written, err := os.ReadFile(stream.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(written), "warning: "); got != 1 {
		t.Fatalf("stream got the notice %d times: %q", got, written)
	}
	var report bytes.Buffer
	if err := writeEngineNoticesOnce(&report, notices); err != nil || !strings.Contains(report.String(), "warning: ") {
		t.Fatalf("a separate report lost the notice: %q %v", &report, err)
	}
}
