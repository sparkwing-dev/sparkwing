package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		"and stops the server and removes the data directory whether the suite passes or " +
		"fails. A server that will not start fails the step with the cause; the suite is " +
		"never skipped. Needs no Docker."
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
	sparkwing.Job(plan, rc.Pipeline, p.run).Timeout(30 * time.Minute)
	return nil
}

const (
	storePostgresVersion    = embeddedpostgres.V17
	storePostgresUser       = "postgres"
	storePostgresPassword   = "postgres"
	storePostgresDatabase   = "postgres"
	storePostgresStartLimit = 2 * time.Minute
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

	root, err := os.MkdirTemp("", "sparkwing-store-postgres-")
	if err != nil {
		return fmt.Errorf("create the postgres temporary root: %w", err)
	}
	port, err := freeLocalPort()
	if err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	layout := storePostgresLayout(root, sparkwing.ToolCacheDir("embedded-postgres"), port)

	sparkwing.Info(ctx, "starting embedded postgres on :%d (data=%s)", layout.Port, layout.Data)
	var serverLog bytes.Buffer
	server := embeddedpostgres.NewDatabase(layout.config(&serverLog))
	if err := server.Start(); err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("start embedded postgres on :%d: %w\n%s",
			layout.Port, err, lastLines(serverLog.String(), 40))
	}
	sparkwing.Annotate(ctx, fmt.Sprintf("embedded postgres ready on :%d", layout.Port))

	suiteErr := runStoreSuiteAgainst(ctx, layout.DSN)
	if suiteErr != nil {
		sparkwing.Info(ctx, "postgres server log:\n%s", lastLines(serverLog.String(), 40))
	}
	stopErr := server.Stop()
	if stopErr != nil {
		stopErr = fmt.Errorf("stop embedded postgres: %w", stopErr)
	}
	removeErr := os.RemoveAll(root)
	if removeErr != nil {
		removeErr = fmt.Errorf("remove the postgres data directory: %w", removeErr)
	}
	return errors.Join(suiteErr, stopErr, removeErr)
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
			Env("SPARKWING_REQUIRE_PG", "1").
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
