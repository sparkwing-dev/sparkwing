package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type multilineRecordLog struct {
	observationLog
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (l *multilineRecordLog) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.observationLog.Emit(rec)
	if rec.Msg == "fixture-ready" && l.cancel != nil {
		l.cancel()
	}
}

func TestMultilineSecretMaskingAcrossExecRecords(t *testing.T) {
	const first = "MII-SYNTHETIC-PRIVATE-COMPONENT-ONE"
	const second = "MII-SYNTHETIC-PRIVATE-COMPONENT-TWO"
	const value = "-----BEGIN PRIVATE KEY-----\n" + first + "\n" + second + "\n-----END PRIVATE KEY-----"
	for _, mode := range []string{"stdout-lf", "stderr-crlf", "split-writes", "no-trailing-newline", "cancel-complete-eof"} {
		t.Run(mode, func(t *testing.T) {
			m := secrets.NewMasker()
			secret := value
			if mode == "stderr-crlf" {
				secret = strings.ReplaceAll(value, "\n", "\r\n") + "\r\n"
			}
			m.Register(secret)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			log := &multilineRecordLog{}
			if mode == "cancel-complete-eof" {
				log.cancel = cancel
			}
			wrapped := wrapNodeLogWithMasker(log, m)
			ctx = sparkwingruntime.WithLogger(ctx, wrapped)
			script := `printf 'ordinary output\n'; printf '%s\n' "$MULTI_SECRET"`
			switch mode {
			case "stderr-crlf":
				script = `printf 'ordinary output\n'; printf '%s' "$MULTI_SECRET" >&2`
			case "split-writes":
				script = `printf 'ordinary output\n'; printf '%s' "$PART_ONE"; printf '%s\n' "$PART_TWO"`
			case "no-trailing-newline":
				script = `printf 'ordinary output\n'; printf '%s' "$MULTI_SECRET"`
			case "cancel-complete-eof":
				script = `printf 'ordinary output\n'; printf '%s' "$COMPONENT"; printf 'fixture-ready\n' >&2; exec sleep 30`
			}
			split := strings.Index(secret, first) + len(first)/2
			_, err := sparkwing.Exec(ctx, "sh", "-c", script).Env("MULTI_SECRET", secret).Env("PART_ONE", secret[:split]).Env("PART_TWO", secret[split:]).Env("COMPONENT", first).Run()
			if mode == "cancel-complete-eof" {
				if err == nil || ctx.Err() != context.Canceled {
					t.Fatalf("cancelled command outcome=%v context=%v", err, ctx.Err())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wrapped.Log("info", first)
			wrapped.Emit(sparkwing.LogRecord{Event: "direct", Msg: second, Attrs: map[string]any{"token": first, "safe": "ordinary attribute"}})
			raw, err := json.Marshal(struct {
				Records []sparkwing.LogRecord
				Lines   []string
			}{log.records, log.lines})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "records.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			persisted, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(persisted), first) || strings.Contains(string(persisted), second) {
				t.Error("multiline secret component reached persisted log records")
			}
			if !strings.Contains(string(persisted), "ordinary output") || !strings.Contains(string(persisted), "ordinary attribute") {
				t.Error("ordinary log content lost")
			}
		})
	}
}
