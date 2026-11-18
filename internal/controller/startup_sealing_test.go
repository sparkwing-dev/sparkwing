package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type startupTestCipher struct {
	*secrets.Cipher
	mu       sync.Mutex
	attempts int
	fail     bool
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
	if c.fail && attempt == 1 {
		return "", errors.New("transient injected failure")
	}
	return c.Cipher.SealBound(p, team, name, scope, shared, masked, plain)
}

func startupController(t *testing.T, c *startupTestCipher) *Server {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	_, err := e.Store.DB().ExecContext(t.Context(), `INSERT INTO cron_schedules(team,id,repo_path,repo_key,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,args) VALUES ('a','startup','/repo','git:example.com/project','nightly','0 * * * *','UTC','skip',0,1,1,1,'{"secret":"controller-startup-canary"}')`)
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
		s.runStartupSealing(t.Context())
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("retry delay=%s", elapsed)
		}
		if cipher.attempts != 2 {
			t.Fatalf("attempts=%d", cipher.attempts)
		}
		if bytes.Count(logs.Bytes(), []byte(`"source":"startup"`)) != 2 || bytes.Contains(logs.Bytes(), []byte("controller-startup-canary")) || bytes.Contains(logs.Bytes(), []byte("transient injected")) {
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
	go func() { defer close(done); s.runStartupSealing(ctx) }()
	<-cipher.entered
	cancel()
	close(cipher.release)
	<-done
	result, err := s.operator.SealPlaintextValues(t.Context(), key)
	if err != nil || result.PlaintextRunArguments != 1 {
		t.Fatalf("cancelled batch committed: %+v %v", result, err)
	}
}

func TestStartupSealingRunsFromServeWith(t *testing.T) {
	key, err := secrets.NewCipher(bytes.Repeat([]byte{4}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cipher := &startupTestCipher{Cipher: key, entered: make(chan struct{}), release: make(chan struct{})}
	s := startupController(t, cipher)
	s.engineController.RunnerPolicy = &SignedRunnerAdmission{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeWith(ctx, s, Listeners{API: ln}) }()
	select {
	case <-cipher.entered:
	case err := <-done:
		t.Fatalf("server exited before sealing: %v", err)
	case <-t.Context().Done():
		t.Fatal("startup pass never ran")
	}
	close(cipher.release)
	request, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+ln.Addr().String()+"/api/v1/health", nil)
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
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
