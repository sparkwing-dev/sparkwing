package controller

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

type holdLog struct {
	slog.Handler
	message string
	entered chan struct{}
	release chan struct{}
}

func (holdLog) Enabled(context.Context, slog.Level) bool { return true }

func (h holdLog) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == h.message {
		close(h.entered)
		<-h.release
	}
	return h.Handler.Handle(ctx, record)
}

func TestServeWithReturnsOnlyAfterItsWorkersExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, _ := engineRuntimeStore(t)
		s := e.Server
		s.engineController.RunnerPolicy = &SignedRunnerAdmission{}
		engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('storage_alarm_database_bytes',?)`, []byte(`1`))
		// safety: storage maintenance stands in for every background worker; it logs
		// this alarm on its first pass and is held there across cancellation.
		held := holdLog{Handler: slog.DiscardHandler, message: "database is larger than its alarm size", entered: make(chan struct{}), release: make(chan struct{})}
		s.logger = slog.New(held)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- ServeWith(ctx, s, Listeners{API: &localControllerListener{connections: make(chan net.Conn), done: make(chan struct{})}})
		}()
		select {
		case <-held.entered:
		case <-time.After(time.Hour):
			t.Fatal("storage maintenance never logged its database alarm")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			close(held.release)
			t.Fatalf("ServeWith returned (err=%v) while storage maintenance was still running", err)
		default:
		}
		close(held.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
