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
	return "Runs `go test ./pkg/store/...` with SPARKWING_TEST_STORE=postgres. " +
		"Uses SPARKWING_TEST_PG_URL when set; otherwise starts an embedded Postgres. " +
		"A failing suite prints the server log tail. The embedded server stops and its " +
		"data directory is removed on completion or interruption. If killed before " +
		"cleanup finishes, the run can leave a sparkwing-store-postgres-* directory under TMPDIR."
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

func (pipeline *StorePostgres) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, runContext sparkwing.RunContext) error {
	sparkwing.Job(plan, runContext.Pipeline, pipeline.run).Timeout(storePostgresPrePushTimeout)
	return nil
}

const (
	storePostgresVersion    = embeddedpostgres.V17
	storePostgresUser       = "postgres"
	storePostgresPassword   = "postgres"
	storePostgresDatabase   = "postgres"
	storePostgresStartLimit = 2 * time.Minute
	// SAFETY: a reserved port can be taken between the reservation and the
	// bind, so one fresh port is tried before the step fails.
	storePostgresStartAttempts  = 2
	storePostgresLogLines       = 40
	storePostgresRootPrefix     = "sparkwing-store-postgres-"
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

func (layout storePostgresPaths) config(logger io.Writer) embeddedpostgres.Config {
	return embeddedpostgres.DefaultConfig().
		Version(storePostgresVersion).
		Username(storePostgresUser).
		Password(storePostgresPassword).
		Database(storePostgresDatabase).
		Port(layout.Port).
		RuntimePath(layout.Runtime).
		DataPath(layout.Data).
		BinariesPath(layout.Binaries).
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
	return uint32(port), nil
}

func (pipeline *StorePostgres) run(ctx context.Context) error {
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
				if err := candidate.Start(); err != nil {
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

func runStorePostgresSuite(ctx context.Context, run storePostgresRun) (err error) {
	defer func() {
		if removeErr := run.remove(); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove the postgres data directory: %w", removeErr))
		}
	}()

	dsn, startErr := run.start()
	if startErr != nil {
		return startErr
	}

	var once sync.Once
	var stopErr error
	stop := func() { once.Do(func() { stopErr = run.stop() }) }
	defer func() {
		stop()
		if stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop embedded postgres: %w", stopErr))
		}
		// SAFETY: Stopping the server flushes its log into the writer.
		if err != nil && run.serverLog != nil && run.report != nil {
			run.report(run.serverLog())
		}
	}()

	interrupts := run.interrupts
	if interrupts == nil {
		// SAFETY: Unhandled signals terminate the process before deferred server cleanup.
		signaled := make(chan os.Signal, 1)
		signal.Notify(signaled, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signaled)
		interrupts = signaled
	}

	done := make(chan error, 1)
	go func() { done <- run.suite(ctx, dsn) }()
	select {
	case suiteErr := <-done:
		return suiteErr
	case <-ctx.Done():
		stop()
		return ctx.Err()
	case receivedSignal := <-interrupts:
		stop()
		return fmt.Errorf("interrupted by %s while the store suite was running", receivedSignal)
	}
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
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
	files := make([]string, 0, len(all))
	for _, file := range all {
		if strings.HasPrefix(file, "pkg/store/") && !isTestdataPath(file) {
			files = append(files, file)
		}
	}
	return files
}
