package logutil_test

import (
	"bytes"
	"flag"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/logutil"
)

func TestBindReadsTheFlagsAndHandlerHonoursThem(t *testing.T) {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	read := logutil.Bind(fs)
	if err := fs.Parse([]string{"--log-format=json", "--log-level=warn"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	h, err := read().Handler(&out)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(h)
	logger.Info("quiet")
	logger.Warn("loud")
	if strings.Contains(out.String(), "quiet") || !strings.Contains(out.String(), `"msg":"loud"`) {
		t.Fatalf("log = %q, want only the warn line, as JSON", out.String())
	}
}

func TestHandlerRefusesAFormatOrLevelItDoesNotKnow(t *testing.T) {
	for _, o := range []logutil.Options{{Format: "yaml"}, {Level: "verbose"}} {
		if _, err := o.Handler(&bytes.Buffer{}); err == nil {
			t.Errorf("Handler(%+v) = nil error, want a refusal", o)
		}
	}
}
