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

var storeRaceName = regexp.MustCompile(`^(Test|Example|Fuzz)[^[:space:]/]*$`)

func storeRaceNames(output string) ([]string, error) {
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !storeRaceName.MatchString(line) {
			return nil, fmt.Errorf("unrecognized store test listing line %q", line)
		}
		names = append(names, line)
	}
	slices.Sort(names)
	if len(names) < storeRaceShardCount {
		return nil, fmt.Errorf("store test listing has %d names, need at least %d", len(names), storeRaceShardCount)
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			return nil, fmt.Errorf("duplicate store test %q", names[i])
		}
	}
	return names, nil
}

func storeRaceShards(names []string) ([][]string, error) {
	shards := make([][]string, storeRaceShardCount)
	for i, name := range names {
		shards[i%storeRaceShardCount] = append(shards[i%storeRaceShardCount], name)
	}
	return shards, checkStoreRaceCoverage(names, shards)
}

func checkStoreRaceCoverage(names []string, shards [][]string) error {
	if len(shards) != storeRaceShardCount {
		return fmt.Errorf("store race has %d shards, want %d", len(shards), storeRaceShardCount)
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		if want[name] {
			return fmt.Errorf("duplicate listed store test %q", name)
		}
		want[name] = true
	}
	seen := make(map[string]bool, len(names))
	for i, shard := range shards {
		if len(shard) == 0 {
			return fmt.Errorf("store race shard %d is empty", i+1)
		}
		for _, name := range shard {
			if !want[name] {
				return fmt.Errorf("store race shard %d has unlisted test %q", i+1, name)
			}
			if seen[name] {
				return fmt.Errorf("store race test %q appears in multiple shards", name)
			}
			seen[name] = true
		}
	}
	for _, name := range names {
		if !seen[name] {
			return fmt.Errorf("store race test %q has no shard", name)
		}
	}
	return nil
}

func storeRacePattern(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func storeRaceDigest(names []string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(names, "\n"))))
}

type storeRaceResult struct {
	shard  int
	count  int
	output sparkwing.ExecResult
	err    error
}

func runStoreRaceShards(ctx context.Context) (runErr error) {
	dir, err := os.MkdirTemp("", "sparkwing-store-race-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean store race binary: %w", err))
		}
	}()
	binary := filepath.Join(dir, "store.test")
	if _, err := sparkwing.Exec(ctx, "go", "test", "-c", "-race", "-o", binary, "./pkg/store").Run(); err != nil {
		return fmt.Errorf("compile store race binary: %w", err)
	}
	listed, err := sparkwing.Exec(ctx, binary, "-test.list", "^(Test|Example|Fuzz)").Dir("pkg/store").Capture()
	if err != nil {
		return fmt.Errorf("list store race tests: %w", err)
	}
	names, err := storeRaceNames(listed.Stdout)
	if err != nil {
		return err
	}
	shards, err := storeRaceShards(names)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "store race: %d listed tests, four exhaustive shards, list sha256=%s", len(names), storeRaceDigest(names))
	for i, shard := range shards {
		sparkwing.Info(ctx, "store race shard %d: %d tests, sha256=%s", i+1, len(shard), storeRaceDigest(shard))
	}
	if _, err := sparkwing.Exec(ctx, "go", "test", "-race", "-count=1", "./pkg/store/internal/storetest").Env("GOMAXPROCS", "1").Run(); err != nil {
		return fmt.Errorf("storetest race package: %w", err)
	}

	shardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan storeRaceResult, storeRaceShardCount)
	for i, shard := range shards {
		go func() {
			result, runErr := sparkwing.Exec(shardCtx, binary,
				"-test.run", storeRacePattern(shard), "-test.count=1", "-test.timeout=55m").
				Dir("pkg/store").Env("GOMAXPROCS", "1").Capture()
			results <- storeRaceResult{shard: i + 1, count: len(shard), output: result, err: runErr}
		}()
	}
	var firstErr error
	for range storeRaceShardCount {
		result := <-results
		for _, line := range strings.Split(strings.TrimSpace(result.output.Stdout+"\n"+result.output.Stderr), "\n") {
			if line != "" {
				sparkwing.Info(ctx, "store race shard %d: %s", result.shard, line)
			}
		}
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("store race shard %d (%d tests): %w", result.shard, result.count, result.err)
			cancel()
		} else if result.err == nil {
			sparkwing.Info(ctx, "store race shard %d passed %d listed tests", result.shard, result.count)
		}
	}
	return firstErr
}
