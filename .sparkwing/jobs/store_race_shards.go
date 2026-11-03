package jobs

import (
	"cmp"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const storeRaceShardCount = 4

// perf: measured per-test race seconds; round-robin left the slowest of four
// hosted shards well past the average, and the gap is the critical path.
//
//go:embed store_race_costs.txt
var storeRaceCostTable string

func parseRaceCosts(table string) (map[string]float64, error) {
	costs := make(map[string]float64)
	for number, line := range strings.Split(table, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !raceTestName.MatchString(fields[1]) {
			return nil, fmt.Errorf("race cost line %d: want \"<seconds> <test>\", got %q", number+1, line)
		}
		seconds, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || seconds < 0 {
			return nil, fmt.Errorf("race cost line %d: bad seconds %q", number+1, fields[0])
		}
		if _, dup := costs[fields[1]]; dup {
			return nil, fmt.Errorf("race cost line %d: duplicate test %q", number+1, fields[1])
		}
		costs[fields[1]] = seconds
	}
	if len(costs) == 0 {
		return nil, errors.New("race cost table is empty")
	}
	return costs, nil
}

// perf: greedy longest-first onto the least loaded shard. A name the table
// does not know costs the mean of the names it does, so a new test lands
// somewhere sensible until the table is refreshed.
func raceCostShards(names []string, costs map[string]float64, shardCount int) ([][]string, error) {
	if len(names) < shardCount {
		return nil, fmt.Errorf("test listing has %d names, need at least %d", len(names), shardCount)
	}
	var known, total float64
	for _, name := range names {
		if cost, ok := costs[name]; ok {
			known++
			total += cost
		}
	}
	fallback := 1.0
	if known > 0 {
		fallback = total / known
	}
	// safety: a zero cost would let two names tie on an empty shard and
	// leave another shard empty.
	const floor = 0.001
	weight := func(name string) float64 {
		cost, ok := costs[name]
		if !ok {
			cost = fallback
		}
		return max(cost, floor)
	}
	ordered := slices.Clone(names)
	slices.SortStableFunc(ordered, func(a, b string) int {
		if c := cmp.Compare(weight(b), weight(a)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	shards := make([][]string, shardCount)
	loads := make([]float64, shardCount)
	for _, name := range ordered {
		lightest := 0
		for i := range loads {
			if loads[i] < loads[lightest] {
				lightest = i
			}
		}
		shards[lightest] = append(shards[lightest], name)
		loads[lightest] += weight(name)
	}
	for _, shard := range shards {
		slices.Sort(shard)
	}
	return shards, checkRaceTestCoverage(names, shards, shardCount)
}

// perf: one of count hosted runners splitting the store race; count 1 runs
// every shard on this host, and count 0 runs none because other lanes do.
type raceStorePart struct{ index, count int }

const (
	maxRaceStoreParts       = 8
	raceStorePartsElsewhere = "none"
)

func parseRaceStorePart(value string) (raceStorePart, error) {
	switch value {
	case "":
		return raceStorePart{index: 1, count: 1}, nil
	case raceStorePartsElsewhere:
		return raceStorePart{}, nil
	}
	index, count, found := strings.Cut(value, "/")
	k, kErr := strconv.Atoi(index)
	n, nErr := strconv.Atoi(count)
	if !found || kErr != nil || nErr != nil || n < 1 || n > maxRaceStoreParts || k < 1 || k > n {
		return raceStorePart{}, fmt.Errorf("race-store part %q: want k/n with 1 <= k <= n <= %d, or %s", value, maxRaceStoreParts, raceStorePartsElsewhere)
	}
	return raceStorePart{index: k, count: n}, nil
}

// safety: every part computes the same exhaustive assignment over all
// count*perPart shards from the same listing and table, then keeps its own
// contiguous run, so the parts together cover each name exactly once.
func (p raceStorePart) shards(all [][]string) [][]string {
	per := len(all) / p.count
	return all[(p.index-1)*per : p.index*per]
}

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

// perf: each lead test is a measured long pole that gets a shard of its own,
// queued ahead of every round-robin shard so it never starts last.
type racePackagePlan struct {
	pkg        string
	shardCount int
	lead       []string
	costs      map[string]float64
	part       raceStorePart
}

func raceLeadShards(names, lead []string, shardCount int) ([][]string, error) {
	listed := make(map[string]bool, len(names))
	for _, name := range names {
		listed[name] = true
	}
	shards := make([][]string, 0, len(lead)+shardCount)
	for _, name := range lead {
		if !listed[name] {
			return nil, fmt.Errorf("lead race test %q is not listed; update the gate's measured long poles", name)
		}
		shards = append(shards, []string{name})
	}
	rest := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return slices.Contains(lead, name) })
	if len(rest) < shardCount {
		return nil, fmt.Errorf("test listing has %d names besides its lead tests, need at least %d", len(rest), shardCount)
	}
	tail, err := raceTestShards(rest, shardCount)
	if err != nil {
		return nil, err
	}
	shards = append(shards, tail...)
	return shards, checkRaceTestCoverage(names, shards, len(shards))
}

type raceShardJob struct {
	pkg, binary, packageDir string
	number                  int
	names                   []string
}

type raceShardResult struct {
	pkg    string
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

func runStoreRaceShards(ctx context.Context, part raceStorePart) error {
	return withProductTestHome(func(home string) error {
		return runStoreRaceShardsAtHome(ctx, home, part)
	})
}

func runStoreRaceShardsAtHome(ctx context.Context, home string, part raceStorePart) error {
	if part.count == 0 {
		// safety: the hosted workflow's coverage test requires a lane for
		// every part whenever pre-release passes this.
		sparkwing.Warn(ctx, "race-store: --race-store-part %s; separate race-store lanes run every part", raceStorePartsElsewhere)
		return nil
	}
	costs, err := parseRaceCosts(storeRaceCostTable)
	if err != nil {
		return err
	}
	if part.index == 1 {
		if _, err := raceExec(ctx, home, "go", "test", "-race", "-count=1", "./internal/store/internal/storetest").Env("GOMAXPROCS", "1").Run(); err != nil {
			return fmt.Errorf("storetest race package: %w", err)
		}
	}
	plan := racePackagePlan{pkg: "./internal/store", shardCount: part.count * storeRaceShardCount, costs: costs, part: part}
	return runRaceShardsAtHome(ctx, home, ".", []racePackagePlan{plan}, raceWorkers{initial: storeRaceShardCount}, storeRaceShardArgs)
}

// perf: extra workers start once ready closes, when the steps that held those
// cores have finished, so the queue never oversubscribes the host.
type raceWorkers struct {
	initial, extra int
	ready          <-chan struct{}
}

func runRaceShardsAtHome(ctx context.Context, home, module string, plans []racePackagePlan, workers raceWorkers, shardArgs func([]string) []string) (runErr error) {
	parallelism := workers.initial
	if parallelism < 1 || workers.extra < 0 {
		return fmt.Errorf("race shard parallelism must be positive")
	}
	dir, err := os.MkdirTemp("", "sparkwing-race-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("clean race binaries: %w", err))
		}
	}()
	var leads, jobs []raceShardJob
	for index, plan := range plans {
		pkg, shardCount := plan.pkg, plan.shardCount
		binary := filepath.Join(dir, fmt.Sprintf("package-%d.test", index))
		if _, err := raceExec(ctx, home, "go", "test", "-c", "-race", "-p", "2", "-o", binary, pkg).Dir(module).Env("GOMAXPROCS", "1").Env("TMPDIR", dir).Run(); err != nil {
			return fmt.Errorf("compile %s race binary: %w", pkg, err)
		}
		packageDir := filepath.Join(module, pkg)
		listed, err := raceExec(ctx, home, binary, "-test.list", "^(Test|Example|Fuzz)").Dir(packageDir).Env("GOMAXPROCS", "1").Env("TMPDIR", dir).Capture()
		if err != nil {
			return fmt.Errorf("list %s race tests: %w", pkg, err)
		}
		names, err := raceTestNames(listed.Stdout, shardCount)
		if err != nil {
			return err
		}
		var shards [][]string
		if plan.costs != nil {
			shards, err = raceCostShards(names, plan.costs, shardCount)
		} else {
			shards, err = raceLeadShards(names, plan.lead, shardCount)
		}
		if err != nil {
			return err
		}
		sparkwing.Info(ctx, "%s race: %d listed tests, exhaustive shards=%d (%d lead), up to %d processes (%d more once the full suites finish), list sha256=%s", pkg, len(names), len(shards), len(plan.lead), parallelism, workers.extra, raceTestDigest(names))
		first := 0
		if plan.part.count > 1 {
			first = (plan.part.index - 1) * len(shards) / plan.part.count
			shards = plan.part.shards(shards)
			sparkwing.Info(ctx, "%s race: part %d of %d runs shards %d-%d; the other parts run the rest", pkg, plan.part.index, plan.part.count, first+1, first+len(shards))
		}
		for i, shard := range shards {
			number := first + i + 1
			sparkwing.Info(ctx, "%s race shard %d: %d tests, sha256=%s", pkg, number, len(shard), raceTestDigest(shard))
			job := raceShardJob{pkg: pkg, binary: binary, packageDir: packageDir, number: number, names: shard}
			if i < len(plan.lead) {
				leads = append(leads, job)
			} else {
				jobs = append(jobs, job)
			}
		}
	}
	jobs = append(leads, jobs...)
	shardCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan raceShardResult, len(jobs))
	pending := make(chan raceShardJob, len(jobs))
	for _, job := range jobs {
		pending <- job
	}
	close(pending)
	work := func() {
		for job := range pending {
			result, runErr := raceExec(shardCtx, home, job.binary, shardArgs(job.names)...).
				Dir(job.packageDir).Env("GOMAXPROCS", "1").Env("TMPDIR", dir).Capture()
			results <- raceShardResult{pkg: job.pkg, shard: job.number, count: len(job.names), output: result, err: runErr}
		}
	}
	for range min(parallelism, len(jobs)) {
		go work()
	}
	if workers.extra > 0 && len(jobs) > parallelism {
		go func() {
			select {
			case <-workers.ready:
			case <-shardCtx.Done():
				return
			}
			if shardCtx.Err() != nil {
				return
			}
			sparkwing.Info(ctx, "race shards: the full suites finished; starting %d more workers", workers.extra)
			for range workers.extra {
				go work()
			}
		}()
	}
	var firstErr error
	for range jobs {
		result := <-results
		for _, line := range strings.Split(strings.TrimSpace(result.output.Stdout+"\n"+result.output.Stderr), "\n") {
			if line != "" {
				sparkwing.Info(ctx, "%s race shard %d: %s", result.pkg, result.shard, line)
			}
		}
		if result.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s race shard %d (%d tests): %w", result.pkg, result.shard, result.count, result.err)
			cancel()
		} else if result.err == nil {
			sparkwing.Info(ctx, "%s race shard %d passed %d listed tests", result.pkg, result.shard, result.count)
		}
	}
	return firstErr
}

// perf: the hosted four-core runner ran every shard past 80 minutes with the
// slow class included; the full suite still runs that class without -race.
func storeRaceShardArgs(shard []string) []string {
	return []string{"-test.run", raceTestPattern(shard), "-test.short", "-test.count=1", "-test.timeout=80m"}
}
