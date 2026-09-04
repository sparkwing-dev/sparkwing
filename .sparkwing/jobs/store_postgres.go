package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type StorePostgres struct{ sparkwing.Base }

func (StorePostgres) ShortHelp() string {
	return "Run the pkg/store suite against Postgres (embedded, no Docker)"
}

func (StorePostgres) Help() string {
	return "Runs `go test ./pkg/store/...` with SPARKWING_TEST_STORE=postgres, so every " +
		"store test that opens through pkg/store/storetest exercises the Postgres dialect " +
		"instead of a SQLite file. When SPARKWING_TEST_PG_URL is already set the run uses " +
		"that server; otherwise it starts an embedded Postgres on a free port, with its data " +
		"directory under a temporary root and its binaries under the persistent tool cache, " +
		"and stops the server and removes the data directory whether the suite passes, " +
		"fails, or is interrupted. A run killed outright before its teardown finishes can leave " +
		"a sparkwing-store-postgres-* directory under TMPDIR. A failing suite prints the tail of the server log, " +
		"which embedded-postgres only makes available once the server has stopped. A server " +
		"that will not start is retried once on a fresh port and then fails the step with " +
		"the cause; the suite is never skipped. Needs no Docker."
}

func (StorePostgres) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the store suite against an embedded Postgres", Command: "sparkwing run store-postgres"},
		{
			Comment: "Run it against a server you already have",
			Command: "SPARKWING_TEST_PG_URL=postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable sparkwing run store-postgres",
		},
	}
}

func (p *StorePostgres) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run).Timeout(storePostgresPrePushTimeout)
	return nil
}

const (
	storePostgresVersion    = embeddedpostgres.V17
	storePostgresUser       = "postgres"
	storePostgresPassword   = "postgres"
	storePostgresDatabase   = "postgres"
	storePostgresStartLimit = 2 * time.Minute
	// safety: a reserved port can be taken between the reservation and the
	// bind, so one fresh port is tried before the step fails.
	storePostgresStartAttempts = 2
	storePostgresLogLines      = 40
	storePostgresRootPrefix    = "sparkwing-store-postgres-"
	// safety: pre-push calls the step directly, so the Plan's timeout does
	// not reach it; both read this.
	storePostgresPrePushTimeout = 30 * time.Minute
)

type storePostgresPaths struct {
	Port     uint32
	Runtime  string
	Data     string
	Binaries string
	DSN      string
}

func storePostgresLayout(root, cache string, port uint32) storePostgresPaths {
	return storePostgresPaths{
		Port:     port,
		Runtime:  filepath.Join(root, "runtime"),
		Data:     filepath.Join(root, "data"),
		Binaries: cache,
		DSN: fmt.Sprintf("postgres://%s:%s@localhost:%d/%s?sslmode=disable",
			storePostgresUser, storePostgresPassword, port, storePostgresDatabase),
	}
}

func (l storePostgresPaths) config(logger io.Writer) embeddedpostgres.Config {
	return embeddedpostgres.DefaultConfig().
		Version(storePostgresVersion).
		Username(storePostgresUser).
		Password(storePostgresPassword).
		Database(storePostgresDatabase).
		Port(l.Port).
		RuntimePath(l.Runtime).
		DataPath(l.Data).
		BinariesPath(l.Binaries).
		Logger(logger).
		StartTimeout(storePostgresStartLimit)
}

func freeLocalPort() (uint32, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a port for postgres: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release the reserved port: %w", err)
	}
	// safety: the port is unbound between here and postgres binding it, so a
	// concurrent listener can still take it; a start failure names the port.
	return uint32(port), nil
}

func (p *StorePostgres) run(ctx context.Context) error {
	if dsn := os.Getenv("SPARKWING_TEST_PG_URL"); dsn != "" {
		sparkwing.Info(ctx, "using the configured SPARKWING_TEST_PG_URL")
		return runStoreSuiteAgainst(ctx, dsn)
	}

	root, err := os.MkdirTemp("", storePostgresRootPrefix)
	if err != nil {
		return fmt.Errorf("create the postgres temporary root: %w", err)
	}

	var serverLog bytes.Buffer
	var server *embeddedpostgres.EmbeddedPostgres
	return runStorePostgresSuite(ctx, storePostgresRun{
		start: func() (string, error) {
			var lastErr error
			for range storePostgresStartAttempts {
				port, err := freeLocalPort()
				if err != nil {
					return "", err
				}
				layout := storePostgresLayout(root, sparkwing.ToolCacheDir("embedded-postgres"), port)
				sparkwing.Info(ctx, "starting embedded postgres on :%d (data=%s)", layout.Port, layout.Data)
				candidate := embeddedpostgres.NewDatabase(layout.config(&serverLog))
				if err := withTempDir(root, candidate.Start); err != nil {
					lastErr = fmt.Errorf("start embedded postgres on :%d: %w", layout.Port, err)
					continue
				}
				server = candidate
				sparkwing.Annotate(ctx, fmt.Sprintf("embedded postgres ready on :%d", layout.Port))
				return layout.DSN, nil
			}
			return "", lastErr
		},
		stop: func() error {
			if server == nil {
				return nil
			}
			return server.Stop()
		},
		remove: func() error { return os.RemoveAll(root) },
		suite:  runStoreSuiteAgainst,
		serverLog: func() string {
			return lastLines(serverLog.String(), storePostgresLogLines)
		},
		report: func(tail string) { sparkwing.Info(ctx, "postgres server log:\n%s", tail) },
	})
}

type storePostgresRun struct {
	interrupts <-chan os.Signal
	start      func() (string, error)
	stop       func() error
	remove     func() error
	suite      func(ctx context.Context, dsn string) error
	serverLog  func() string
	report     func(tail string)
}

func runStorePostgresSuite(ctx context.Context, r storePostgresRun) (err error) {
	defer func() {
		if removeErr := r.remove(); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove the postgres data directory: %w", removeErr))
		}
	}()

	dsn, startErr := r.start()
	if startErr != nil {
		return startErr
	}

	var once sync.Once
	var stopErr error
	stop := func() { once.Do(func() { stopErr = r.stop() }) }
	defer func() {
		stop()
		if stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop embedded postgres: %w", stopErr))
		}
		// safety: embedded-postgres copies the server log into the writer
		// only inside Start and Stop, so the tail is read after the stop.
		if err != nil && r.serverLog != nil && r.report != nil {
			r.report(r.serverLog())
		}
	}()

	interrupts := r.interrupts
	if interrupts == nil {
		// safety: pkg/runner installs no handler, so an unhandled SIGINT
		// kills this process before any defer can stop the server.
		signalled := make(chan os.Signal, 1)
		signal.Notify(signalled, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signalled)
		interrupts = signalled
	}

	done := make(chan error, 1)
	go func() { done <- r.suite(ctx, dsn) }()
	select {
	case suiteErr := <-done:
		return suiteErr
	case <-ctx.Done():
		stop()
		return ctx.Err()
	case sig := <-interrupts:
		stop()
		return fmt.Errorf("interrupted by %s while the store suite was running", sig)
	}
}

func withTempDir(dir string, run func() error) error {
	// safety: embedded-postgres creates its own log file under TMPDIR and
	// never removes it, so the start runs with TMPDIR inside the root that
	// this pipeline deletes.
	previous, had := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", dir); err != nil {
		return err
	}
	defer func() {
		if had {
			_ = os.Setenv("TMPDIR", previous)
			return
		}
		_ = os.Unsetenv("TMPDIR")
	}()
	return run()
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func runStoreSuiteAgainst(ctx context.Context, dsn string) error {
	return withGoTestScratch(func(testRoot string) error {
		_, err := sparkwing.Bash(ctx, storePostgresGoCommand(runtime.NumCPU())).
			Env("TMPDIR", testRoot).
			Env("SPARKWING_TEST_STORE", "postgres").
			Env("SPARKWING_TEST_PG_URL", dsn).
			Run()
		if err != nil {
			return fmt.Errorf("store suite against postgres: %w", err)
		}
		sparkwing.Info(ctx, "go test ./pkg/store/...: passed against postgres")
		return nil
	})
}

func storePostgresGoCommand(cpuCount int) string {
	return boundedGoCommand(cpuCount, "test", "-count=1 ./pkg/store/...")
}

func init() {
	sparkwing.Register("store-postgres", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &StorePostgres{} })
}

func runStorePostgresIfTouched(ctx context.Context) error {
	files, scope, err := changeScope(ctx, "pkg/store file(s)", storeFiles)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "store-postgres: %s", scope)
	if len(files) == 0 {
		sparkwing.Info(ctx, "store-postgres: pkg/store unchanged; pre-push runs the suite against postgres regardless")
		return nil
	}
	return (&StorePostgres{}).run(ctx)
}

func storeFiles(all []string) []string {
	out := make([]string, 0, len(all))
	for _, f := range all {
		if strings.HasPrefix(f, "pkg/store/") && !isTestdataPath(f) {
			out = append(out, f)
		}
	}
	return out
}
