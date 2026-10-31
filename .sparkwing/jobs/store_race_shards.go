package jobs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const storeRaceShardCount = 4

var raceTestName = regexp.MustCompile(`^(Test|Example|Fuzz)[^[:space:]/]*$`)

func raceTestNames(output string, shardCount int) ([]string, error) {
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !raceTestName.MatchString(line) {
			return nil, fmt.Errorf("unrecognized test listing line %q", line)
		}
		names = append(names, line)
	}
	slices.Sort(names)
	if len(names) < shardCount {
		return nil, fmt.Errorf("test listing has %d names, need at least %d", len(names), shardCount)
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			return nil, fmt.Errorf("duplicate test %q", names[i])
		}
	}
	return names, nil
}

func raceTestShards(names []string, shardCount int) ([][]string, error) {
	shards := make([][]string, shardCount)
	for i, name := range names {
		shards[i%shardCount] = append(shards[i%shardCount], name)
	}
	return shards, checkRaceTestCoverage(names, shards, shardCount)
}

func checkRaceTestCoverage(names []string, shards [][]string, shardCount int) error {
	if len(shards) != shardCount {
		return fmt.Errorf("race has %d shards, want %d", len(shards), shardCount)
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		if want[name] {
			return fmt.Errorf("duplicate listed test %q", name)
		}
		want[name] = true
	}
	seen := make(map[string]bool, len(names))
	for i, shard := range shards {
		if len(shard) == 0 {
			return fmt.Errorf("race shard %d is empty", i+1)
		}
		for _, name := range shard {
			if !want[name] {
				return fmt.Errorf("race shard %d has unlisted test %q", i+1, name)
			}
			if seen[name] {
				return fmt.Errorf("race test %q appears in multiple shards", name)
			}
			seen[name] = true
		}
	}
	for _, name := range names {
		if !seen[name] {
			return fmt.Errorf("race test %q has no shard", name)
		}
	}
	return nil
}

func raceTestPattern(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func raceTestDigest(names []string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(names, "\n"))))
}

type raceShardResult struct {
	shard  int
	count  int
	output sparkwing.ExecResult
	err    error
}

func raceExec(ctx context.Context, home, name string, args ...string) *sparkwing.Cmd {
	envArgs := make([]string, 0, 2*len(productTestUnset)+len(args)+4)
	for _, name := range productTestUnset {
		envArgs = append(envArgs, "-u", name)
	}
	envArgs = append(envArgs, productTestHomeVar+"="+home, "GOWORK=off", name)
	envArgs = append(envArgs, args...)
	return sparkwing.Exec(ctx, "env", envArgs...)
}

func runStoreRaceShards(ctx context.Context) error {
	return withProductTestHome(func(home string) error {
		return runStoreRaceShardsAtHome(ctx, home)
	})
}

func runStoreRaceShardsAtHome(ctx context.Context, home string) error {
	if _, err := raceExec(ctx, home, "go", "test", "-race", "-count=1", "./internal/store/internal/storetest").Env("GOMAXPROCS", "1").Run(); err != nil {
		return fmt.Errorf("storetest race package: %w", err)
	}
	return runRaceShardsAtHome(ctx, home, ".", "./internal/store", storeRaceShardCount, storeRaceShardArgs)
}

func runRaceShardsAtHome(ctx context.Context, home, module, pkg string, shardCount int, shardArgs func([]string) []string) (runErr error) {
	dir, err := os.MkdirTemp("", "sparkwing-race-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean %s race binary: %w", pkg, err))
		}
	}()
	binary := filepath.Join(dir, "package.test")
	if _, err := raceExec(ctx, home, "go", "test", "-c", "-race", "-o", binary, pkg).Dir(module).Env("TMPDIR", dir).Run(); err != nil {
		return fmt.Errorf("compile %s race binary: %w", pkg, err)
	}
	packageDir := filepath.Join(module, pkg)
	listed, err := raceExec(ctx, home, binary, "-test.list", "^(Test|Example|Fuzz)").Dir(packageDir).Env("TMPDIR", dir).Capture()
	if err != nil {
		return fmt.Errorf("list %s race tests: %w", pkg, err)
	}
	names, err := raceTestNames(listed.Stdout, shardCount)
	if err != nil {
		return err
	}
	shards, err := raceTestShards(names, shardCount)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "%s race: %d listed tests, %d exhaustive shards, list sha256=%s", pkg, len(names), shardCount, raceTestDigest(names))
	for i, shard := range shards {
		sparkwing.Info(ctx, "%s race shard %d: %d tests, sha256=%s", pkg, i+1, len(shard), raceTestDigest(shard))
	}

	shardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan raceShardResult, shardCount)
	for i, shard := range shards {
		go func() {
			result, runErr := raceExec(shardCtx, home, binary, shardArgs(shard)...).
				Dir(packageDir).Env("GOMAXPROCS", "1").Env("TMPDIR", dir).Capture()
			results <- raceShardResult{shard: i + 1, count: len(shard), output: result, err: runErr}
		}()
	}
	var firstErr error
	for range shardCount {
		result := <-results
		for _, line := range strings.Split(strings.TrimSpace(result.output.Stdout+"\n"+result.output.Stderr), "\n") {
			if line != "" {
				sparkwing.Info(ctx, "%s race shard %d: %s", pkg, result.shard, line)
			}
		}
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s race shard %d (%d tests): %w", pkg, result.shard, result.count, result.err)
			cancel()
		} else if result.err == nil {
			sparkwing.Info(ctx, "%s race shard %d passed %d listed tests", pkg, result.shard, result.count)
		}
	}
	return firstErr
}

// perf: the hosted four-core runner ran every shard past 80 minutes with the
// slow class included; the full suite still runs that class without -race.
func storeRaceShardArgs(shard []string) []string {
	return []string{"-test.run", raceTestPattern(shard), "-test.short", "-test.count=1", "-test.timeout=80m"}
}
