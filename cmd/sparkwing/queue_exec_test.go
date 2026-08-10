package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const queueExecWait = 10 * time.Second

func TestQueueExecWaitsInDaemonBeforeStartingCommand(t *testing.T) {
	home := queueHome(t)
	serveQueueDaemon(t, home)

	holderClient, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("connect holder: %v", err)
	}
	holder, err := holderClient.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:          "existing-bootstrap",
		SemaphoresOnly: true,
		Semaphores: []wingwire.SemaphoreClaim{{
			Name: "bootstrap", Cost: 1, Capacity: 1, Policy: wingwire.PolicyQueue,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Release() })

	marker := filepath.Join(t.TempDir(), "started")
	result := make(chan error, 1)
	go func() {
		result <- runQueue([]string{
			"exec", "--home", home,
			"--run-id", "waiting-bootstrap",
			"--semaphore", "bootstrap",
			"--", os.Args[0], "-test.run=TestQueueExecHelperProcess", "--", marker, "23",
		})
	}()

	deadline := time.Now().Add(queueExecWait)
	for {
		qs, queryErr := wingdclient.Query(context.Background(), wingdclient.Options{Home: home})
		if queryErr != nil {
			t.Fatalf("query queue: %v", queryErr)
		}
		if len(qs.Waiters) == 1 && qs.Waiters[0].RunID == "waiting-bootstrap" {
			break
		}
		select {
		case runErr := <-result:
			t.Fatalf("queue exec returned before admission: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue exec never became visible: %+v", qs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command started before admission: %v", err)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	select {
	case runErr := <-result:
		if code := exitCodeFor(runErr); code != 23 {
			t.Fatalf("queue exec exit code = %d, want child status 23: %v", code, runErr)
		}
	case <-time.After(queueExecWait):
		t.Fatal("queue exec did not run after promotion")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("promoted command did not run: %v", err)
	}
}

func TestQueueExecHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
		}
	}
	if separator < 0 || len(os.Args) != separator+3 {
		return
	}
	if err := os.WriteFile(os.Args[separator+1], []byte("started"), 0o600); err != nil {
		os.Exit(97)
	}
	code, err := strconv.Atoi(os.Args[separator+2])
	if err != nil {
		os.Exit(98)
	}
	os.Exit(code)
}
