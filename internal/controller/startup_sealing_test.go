package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type startupTestCipher struct {
	*secrets.Cipher
	mu       sync.Mutex
	attempts int
	fail     bool
	refused  bool
	entered  chan struct{}
	release  chan struct{}
}

func (c *startupTestCipher) SealBound(p secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error) {
	c.mu.Lock()
	c.attempts++
	attempt := c.attempts
	c.mu.Unlock()
	if c.entered != nil && attempt == 1 {
		close(c.entered)
		<-c.release
	}
	if c.refused {
		return "", secrets.ErrKeyRefused
	}
	if c.fail && attempt == 1 {
		return "", errors.New("transient injected failure")
	}
	return c.Cipher.SealBound(p, team, name, scope, shared, masked, plain)
}

func startupController(t *testing.T, c *startupTestCipher) *Server {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	_, err := teststore.Exec(t.Context(), e.Store, `INSERT INTO cron_schedules(team,id,repo_path,repo_key,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,args) VALUES ('a','startup','/repo','git:example.com/project','nightly','0 * * * *','UTC','skip',0,1,1,1,'{"secret":"controller-startup-canary"}')`)
	if err != nil {
		t.Fatal(err)
	}
	return e.Server.WithSecretsCipher(c)
}

func TestStartupSealingRetriesAndSanitizes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		key, err := secrets.NewCipher(bytes.Repeat([]byte{4}, secrets.KeySize))
		if err != nil {
			t.Fatal(err)
		}
		cipher := &startupTestCipher{Cipher: key, fail: true}
		s := startupController(t, cipher)
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		start := time.Now()
		s.RunStartupSealing(t.Context())
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("retry delay=%s", elapsed)
		}
		if cipher.attempts != 2 {
			t.Fatalf("attempts=%d", cipher.attempts)
		}
		if !bytes.Contains(logs.Bytes(), []byte(`"reason":"store_error"`)) || bytes.Count(logs.Bytes(), []byte(`"source":"startup"`)) != 2 || bytes.Contains(logs.Bytes(), []byte("controller-startup-canary")) || bytes.Contains(logs.Bytes(), []byte("transient injected")) {
			t.Fatalf("unsafe or missing summaries: %s", logs.String())
		}
	})
}

func TestStartupSealingCancellationRollsBackBatch(t *testing.T) {
	key, err := secrets.NewCipher(bytes.Repeat([]byte{4}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cipher := &startupTestCipher{Cipher: key, entered: make(chan struct{}), release: make(chan struct{})}
	s := startupController(t, cipher)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.RunStartupSealing(ctx) }()
	<-cipher.entered
	cancel()
	close(cipher.release)
	<-done
	result, err := s.operator.SealPlaintextValues(t.Context(), key)
	if err != nil || result.PlaintextRunArguments != 1 {
		t.Fatalf("cancelled batch committed: %+v %v", result, err)
	}
}

type startupBarrierLog struct {
	slog.Handler
	entered chan struct{}
	release chan struct{}
}

func (h startupBarrierLog) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "startup plaintext sealing" {
		close(h.entered)
		<-h.release
	}
	return h.Handler.Handle(ctx, record)
}

func TestStartupSealingRunsFromServeWith(t *testing.T) {
	key, err := secrets.NewCipher(bytes.Repeat([]byte{4}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cipher := &startupTestCipher{Cipher: key, entered: make(chan struct{}), release: make(chan struct{})}
	s := startupController(t, cipher)
	summaryEntered, summaryRelease := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseSummary := func() { releaseOnce.Do(func() { close(summaryRelease) }) }
	defer releaseSummary()
	s.logger = slog.New(startupBarrierLog{Handler: s.logger.Handler(), entered: summaryEntered, release: summaryRelease})
	s.engineController.RunnerPolicy = &SignedRunnerAdmission{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeWith(ctx, s, Listeners{API: ln}) }()
	select {
	case <-cipher.entered:
	case err := <-done:
		t.Fatalf("server exited before sealing: %v", err)
	case <-ctx.Done():
		t.Fatal("startup pass never ran")
	}
	close(cipher.release)
	select {
	case <-summaryEntered:
	case <-ctx.Done():
		t.Fatal("startup summary never ran")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	releaseSummary()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStartupSealingNoKeyStops(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				key, err := secrets.NewCipher(bytes.Repeat([]byte{4}, secrets.KeySize))
				if err != nil {
					t.Fatal(err)
				}
				cipher := &startupTestCipher{Cipher: key, refused: refused}
				e, _ := engineRuntimeStore(t)
				s := startupController(t, cipher)
				s.operator = e.Store.AsOperator()
				if refused {
					if _, err := teststore.Exec(t.Context(), e.Store, `INSERT INTO cron_schedules(team,id,repo_path,repo_key,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,args) VALUES ('a','startup','/repo','git:example.com/project','nightly','0 * * * *','UTC','skip',0,1,1,1,'{"secret":"canary"}')`); err != nil {
						t.Fatal(err)
					}
				} else {
					s.secretsCipher = nil
				}
				var logs bytes.Buffer
				s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); s.RunStartupSealing(ctx) }()
				synctest.Wait()
				select {
				case <-done:
				default:
					cancel()
					t.Fatal("no-key worker kept retrying")
				}
				if bytes.Count(logs.Bytes(), []byte(`"source":"startup"`)) != 1 || !bytes.Contains(logs.Bytes(), []byte(`"outcome":"skipped"`)) || !bytes.Contains(logs.Bytes(), []byte(`"reason":"no_key"`)) {
					t.Fatalf("summary: %s", logs.String())
				}
				var count int
				if err := teststore.SQL(t, e.Store).QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE kind='secrets.startup_sealed' AND attrs LIKE '%"outcome":"skipped"%'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("audit count=%d err=%v", count, err)
				}
			})
		})
	}
}

func TestStartupSealingKeylessServeWithStaysReady(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	s := e.Server
	s.secretsCipher = nil
	s.engineController.RunnerPolicy = &SignedRunnerAdmission{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var logs bytes.Buffer
	s.logger = slog.New(startupBarrierLog{Handler: slog.NewJSONHandler(&logs, nil), entered: entered, release: release})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeWith(ctx, s, Listeners{API: ln}) }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("server exited before summary: %v", err)
	case <-ctx.Done():
		t.Fatal("keyless startup worker never ran")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/api/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	unblock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if bytes.Count(logs.Bytes(), []byte(`"source":"startup"`)) != 1 || !bytes.Contains(logs.Bytes(), []byte(`"outcome":"skipped"`)) {
		t.Fatalf("summary: %s", logs.String())
	}
	var count int
	if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT count(*) FROM audit_events WHERE kind='secrets.startup_sealed' AND attrs LIKE '%"outcome":"skipped"%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}

func TestStartupSealingNoKeyRetriesAudit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _ := engineRuntimeStore(t)
		s := e.Server
		s.secretsCipher = nil
		db := teststore.SQL(t, e.Store)
		if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_startup_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(FAIL,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); s.RunStartupSealing(ctx) }()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("worker stopped before recording skipped audit")
		default:
		}
		if _, err := db.ExecContext(t.Context(), `DROP TRIGGER reject_startup_audit`); err != nil {
			t.Fatal(err)
		}
		<-done
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE kind='secrets.startup_sealed' AND attrs LIKE '%"outcome":"skipped"%'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("count=%d err=%v", count, err)
		}
		if bytes.Count(logs.Bytes(), []byte(`"outcome":"skipped"`)) != 1 {
			t.Fatalf("summaries: %s", logs.String())
		}
	})
}

func TestStartupSealingNoKeyAuditCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _ := engineRuntimeStore(t)
		s := e.Server
		s.secretsCipher = nil
		if _, err := teststore.SQL(t, e.Store).ExecContext(t.Context(), `CREATE TRIGGER reject_startup_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(FAIL,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); s.RunStartupSealing(ctx) }()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("worker stopped before cancellation")
		default:
		}
		cancel()
		<-done
	})
}

func TestStartupSealingNoKeyAuditWarnsEachFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _ := engineRuntimeStore(t)
		s := e.Server
		s.secretsCipher = nil
		db := teststore.SQL(t, e.Store)
		if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER reject_startup_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(FAIL,'private-audit-error-canary'); END`); err != nil {
			t.Fatal(err)
		}
		var logs SyncBuffer
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); s.RunStartupSealing(ctx) }()
		synctest.Wait()
		if strings.Count(logs.String(), `"level":"WARN"`) != 1 {
			cancel()
			<-done
			t.Fatalf("first warning: %s", logs.String())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if strings.Count(logs.String(), `"level":"WARN"`) != 2 {
			cancel()
			<-done
			t.Fatalf("second warning: %s", logs.String())
		}
		if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_startup_audit`); err != nil {
			t.Fatal(err)
		}
		<-done
		if strings.Count(logs.String(), `"reason":"audit_error"`) != 2 || strings.Contains(logs.String(), "private-audit-error-canary") || strings.Count(logs.String(), `"outcome":"skipped"`) != 1 {
			t.Fatalf("unsafe or missing logs: %s", logs.String())
		}
	})
}
