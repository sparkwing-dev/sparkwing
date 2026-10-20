package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The helper runs the real startup and fail-stop path in a separate process.
func TestControllerPostgresFailStopAndStandby(t *testing.T) {
	const helper = "--controller-process"
	for i, arg := range os.Args {
		if arg == helper {
			defer func() { _ = os.WriteFile(filepath.Join(os.Getenv("SPARKWING_HOME"), "drained"), nil, 0o600) }()
			if err := run(os.Args[i+1:]); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	dsn := os.Getenv("SPARKWING_TEST_PG_URL")
	if dsn == "" {
		t.Skip("SPARKWING_TEST_PG_URL not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	schema := "sw_controller_" + strings.ToLower(rand.Text())
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupDB, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = cleanupDB.Close() }()
		if _, err := cleanupDB.ExecContext(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	type process struct {
		cmd  *exec.Cmd
		done chan struct{}
		err  error
		home string
		log  *bytes.Buffer
	}
	start := func(name string) *process {
		t.Helper()
		home, creds := t.TempDir(), t.TempDir()
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		q.Set("application_name", name)
		u.RawQuery = q.Encode()
		if err := os.WriteFile(filepath.Join(creds, credPGURL), []byte(u.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControllerPostgresFailStopAndStandby$", "--", helper,
			"--store=postgres", "--addr=127.0.0.1:0", "--credentials-dir="+creds)
		cmd.Env = append(os.Environ(), "SPARKWING_HOME="+home)
		log := new(bytes.Buffer)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		p := &process{cmd: cmd, done: make(chan struct{}), home: home, log: log}
		go func() { p.err = cmd.Wait(); close(p.done) }()
		t.Cleanup(func() { _ = cmd.Process.Kill(); <-p.done })
		return p
	}
	wait := func(p *process, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-p.done:
				t.Fatalf("controller exited during startup: %v\n%s", p.err, p.log)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			default:
			}
		}
	}
	firstName, secondName := schema+"_active", schema+"_standby"
	first := start(firstName)
	wait(first, func() bool { _, err := os.Stat(filepath.Join(first.home, "deployment.json")); return err == nil })
	second := start(secondName)
	var firstPID int
	wait(second, func() bool {
		return db.QueryRowContext(ctx, `SELECT l.pid FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE a.application_name = $1 AND l.locktype = 'advisory' AND NOT l.granted`, secondName).Scan(&firstPID) == nil
	})
	if _, err := os.Stat(filepath.Join(second.home, "deployment.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("standby reached mutable startup: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT l.pid FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE a.application_name = $1 AND l.locktype = 'advisory' AND l.granted`, firstName).Scan(&firstPID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, firstPID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
		if exit, ok := first.err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(first.log.String(), "controller lock lost") {
			t.Fatalf("active did not fail-stop: %v\n%s", first.err, first.log)
		}
	case <-ctx.Done():
		t.Fatal("active controller did not exit after losing its lock connection")
	}
	if _, err := os.Stat(filepath.Join(first.home, "drained")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active controller drained after losing its lock: %v", err)
	}
	wait(second, func() bool { _, err := os.Stat(filepath.Join(second.home, "deployment.json")); return err == nil })
	if err := second.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-second.done
}
