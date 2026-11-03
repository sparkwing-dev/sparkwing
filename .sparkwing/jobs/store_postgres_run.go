package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// safety: embedded-postgres already links this driver; the job uses it only
	// to create and drop the shard databases.
	_ "github.com/lib/pq"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const storePostgresDatabasePrefix = "sparkwing_store_shard_"

func runStoreSuiteAgainst(ctx context.Context, dsn string) error {
	return withGoTestScratch(func(testRoot string) error {
		return withProductTestHome(func(home string) error {
			if err := runStorePostgresShards(ctx, home, testRoot, dsn, storePostgresShardCount(currentHost())); err != nil {
				return fmt.Errorf("store suite against postgres: %w", err)
			}
			sparkwing.Info(ctx, "store suite: passed against postgres")
			return nil
		})
	})
}

// perf: each shard is one GOMAXPROCS=1 process, so the count follows the cores
// a Go step may hold; the hosted four-core runner carries no neighbor and runs four.
func storePostgresShardCount(h hostShape) int {
	if h.singleTenant && h.cpus == overlappedGoSuiteCPUs {
		return h.cpus
	}
	return goStepParallelism(h)
}

func storePostgresExec(ctx context.Context, home, scratch, dsn, name string, args ...string) *sparkwing.Cmd {
	return raceExec(ctx, home, name, args...).
		Env("TMPDIR", scratch).
		Env("SPARKWING_TEST_STORE", "postgres").
		Env(storePostgresURLEnv, dsn)
}

func runStorePostgresShards(ctx context.Context, home, scratch, dsn string, shardCount int) (runErr error) {
	if shardCount < 1 {
		return fmt.Errorf("store-postgres shard count must be positive")
	}
	limit := strconv.Itoa(shardCount)
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
	names, err := raceTestNames(listed.Stdout, shardCount)
	if err != nil {
		return err
	}
	sel, err := selectStorePostgresTests(names, storePostgresPackageDir, storePostgresSQLiteOnlyFiles(storePostgresSQLiteOnlyList))
	if err != nil {
		return err
	}
	shards, err := raceTestShards(sel.run, shardCount)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "store-postgres: %d listed tests (sha256=%s); %d run on postgres (sha256=%s) in %d shards; %d SQLite-only or duplicate tests left to the full Go suite (sha256=%s)",
		len(names), raceTestDigest(names), len(sel.run), raceTestDigest(sel.run), len(shards), len(sel.skipped), raceTestDigest(sel.skipped))

	dsns, drop, err := createStorePostgresDatabases(ctx, dsn, len(shards))
	if err != nil {
		return err
	}
	defer func() {
		if err := drop(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("drop the shard databases: %w", err))
		}
	}()

	shardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan raceShardResult, len(shards))
	for i, shard := range shards {
		sparkwing.Info(ctx, "store-postgres shard %d: %d tests, sha256=%s", i+1, len(shard), raceTestDigest(shard))
		go func() {
			result, err := storePostgresExec(shardCtx, home, scratch, dsns[i], binary, storePostgresShardArgs(shard)...).
				Dir(storePostgresPackageDir).Env("GOMAXPROCS", "1").Capture()
			results <- raceShardResult{pkg: storePostgresPackage, shard: i + 1, count: len(shard), output: result, err: err}
		}()
	}
	var firstErr error
	for range shards {
		result := <-results
		for _, line := range strings.Split(strings.TrimSpace(result.output.Stdout+"\n"+result.output.Stderr), "\n") {
			if line != "" {
				sparkwing.Info(ctx, "store-postgres shard %d: %s", result.shard, line)
			}
		}
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("store-postgres shard %d (%d tests): %w", result.shard, result.count, result.err)
			cancel()
		} else if result.err == nil {
			sparkwing.Info(ctx, "store-postgres shard %d passed %d listed tests", result.shard, result.count)
		}
	}
	return firstErr
}

// safety: inside the step's thirty minutes, so a hung test reports its stack before the step is killed.
func storePostgresShardArgs(names []string) []string {
	return []string{"-test.run", raceTestPattern(names), "-test.count=1", "-test.timeout=25m"}
}

// safety: the store's migration advisory lock is scoped to a database, so
// shards sharing one would queue every fixture's schema apply behind each other.
func createStorePostgresDatabases(ctx context.Context, baseDSN string, count int) (dsns []string, drop func() error, err error) {
	if _, err := storePostgresDatabaseURL(baseDSN, storePostgresDatabasePrefix); err != nil {
		return nil, nil, err
	}
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return nil, nil, err
	}
	admin, err := sql.Open("postgres", baseDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres to create the shard databases: %w", err)
	}
	var created []string
	drop = func() error {
		// safety: the run's context may already be cancelled, and FORCE ends
		// connections a killed shard left behind.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		var errs []error
		for _, name := range created {
			if _, err := admin.ExecContext(dropCtx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
				errs = append(errs, fmt.Errorf("drop database %s: %w", name, err))
			}
		}
		return errors.Join(append(errs, admin.Close())...)
	}
	for i := range count {
		name := fmt.Sprintf("%s%s_%d", storePostgresDatabasePrefix, hex.EncodeToString(token), i+1)
		if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
			return nil, nil, errors.Join(fmt.Errorf("create database %s: %w", name, err), drop())
		}
		created = append(created, name)
		shardDSN, err := storePostgresDatabaseURL(baseDSN, name)
		if err != nil {
			return nil, nil, errors.Join(err, drop())
		}
		dsns = append(dsns, shardDSN)
	}
	return dsns, drop, nil
}

func storePostgresDatabaseURL(baseDSN, database string) (string, error) {
	u, err := url.Parse(baseDSN)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// URL so each shard can get a database of its own", storePostgresURLEnv)
	}
	query := u.Query()
	// safety: pgx lets a service entry name the database, which would put every
	// shard back in one database and its migration lock.
	if query.Has("service") || query.Has("servicefile") {
		return "", fmt.Errorf("%s names a pg_service entry, which can choose the database; give the server as a plain postgres:// URL so each shard can get a database of its own", storePostgresURLEnv)
	}
	// safety: pgx reads dbname and database query options over the URL path.
	query.Del("dbname")
	query.Del("database")
	u.Path, u.RawPath, u.RawQuery = "/"+database, "", query.Encode()
	return u.String(), nil
}
