package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runStoreSuiteAgainst(ctx context.Context, dsn string) error {
	return withGoTestScratch(func(testRoot string) error {
		return withProductTestHome(func(home string) error {
			if err := runStorePostgresSelected(ctx, home, testRoot, dsn, goStepParallelism(currentHost())); err != nil {
				return fmt.Errorf("store suite against postgres: %w", err)
			}
			sparkwing.Info(ctx, "store suite: passed against postgres")
			return nil
		})
	})
}

func storePostgresExec(ctx context.Context, home, scratch, dsn, name string, args ...string) *sparkwing.Cmd {
	return raceExec(ctx, home, name, args...).
		Env("TMPDIR", scratch).
		Env("SPARKWING_TEST_STORE", "postgres").
		Env(storePostgresURLEnv, dsn)
}

func runStorePostgresSelected(ctx context.Context, home, scratch, dsn string, buildParallelism int) error {
	limit := strconv.Itoa(buildParallelism)
	if _, err := storePostgresExec(ctx, home, scratch, dsn, "go", "test", "-p", limit, "-count=1", "./internal/store/internal/storetest", "./internal/store/teststore").
		Env("GOMAXPROCS", limit).Run(); err != nil {
		return fmt.Errorf("store helper packages: %w", err)
	}
	binary := filepath.Join(scratch, "store-postgres.test")
	if _, err := raceExec(ctx, home, "go", "test", "-c", "-p", limit, "-o", binary, storePostgresPackage).
		Env("GOMAXPROCS", limit).Env("TMPDIR", scratch).Run(); err != nil {
		return fmt.Errorf("compile the store test binary: %w", err)
	}
	listed, err := raceExec(ctx, home, binary, "-test.list", "^(Test|Example|Fuzz)").
		Dir(storePostgresPackageDir).Env("TMPDIR", scratch).Capture()
	if err != nil {
		return fmt.Errorf("list store tests: %w", err)
	}
	names, err := raceTestNames(listed.Stdout, 1)
	if err != nil {
		return err
	}
	sel, err := selectStorePostgresTests(names, storePostgresPackageDir)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "store-postgres: %d listed tests (sha256=%s); %d run on postgres (sha256=%s); %d SQLite-only or duplicate tests left to the full Go suite (sha256=%s)",
		len(names), raceTestDigest(names), len(sel.run), raceTestDigest(sel.run), len(sel.skipped), raceTestDigest(sel.skipped))
	result, runErr := storePostgresExec(ctx, home, scratch, dsn, binary, storePostgresShardArgs(sel.run)...).
		Dir(storePostgresPackageDir).Env("GOMAXPROCS", "1").Capture()
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout+"\n"+result.Stderr), "\n") {
		if line != "" {
			sparkwing.Info(ctx, "store-postgres: %s", line)
		}
	}
	if runErr != nil {
		return fmt.Errorf("%d selected store tests: %w", len(sel.run), runErr)
	}
	return nil
}

func storePostgresShardArgs(names []string) []string {
	return []string{"-test.run", raceTestPattern(names), "-test.count=1", "-test.timeout=40m"}
}
