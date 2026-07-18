package storeurl

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenStateOutbox_FailsClosedWhenDirUncreatable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	t.Setenv("SPARKWING_HOME", filepath.Join(blocker, "home"))

	opts, err := openStateOutbox(nil)
	if err == nil {
		t.Fatal("openStateOutbox returned nil error when the outbox dir cannot be created")
	}
	if opts != nil {
		t.Errorf("openStateOutbox returned options on failure: %v", opts)
	}
	if !strings.Contains(err.Error(), "outbox dir") {
		t.Errorf("error %q does not name the offending step", err)
	}
}

func TestLogOutboxUnavailable_WarnsWithReason(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cause := errors.New("disk is read-only")
	logOutboxUnavailable(log, cause)

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected a WARN record, got %q", out)
	}
	if !strings.Contains(out, "not in effect") {
		t.Errorf("warning does not state resilience is not in effect: %q", out)
	}
	if !strings.Contains(out, "disk is read-only") {
		t.Errorf("warning does not carry the underlying cause: %q", out)
	}
}
