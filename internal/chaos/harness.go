package chaos

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type Config struct {
	Seed int64

	Duration time.Duration

	MaxActors int

	Settle time.Duration

	EnableCLI bool

	FaultBudget float64

	DaemonIdleMS  int
	DaemonGraceMS int

	DaemonCores float64

	MaxOwnedProcesses int

	MaxZombieDrain time.Duration

	OracleTimeout time.Duration
}

func CIConfig(seed int64) Config {
	return Config{
		Seed:              seed,
		Duration:          25 * time.Second,
		MaxActors:         10,
		Settle:            6 * time.Second,
		EnableCLI:         true,
		FaultBudget:       0.6,
		DaemonIdleMS:      3000,
		DaemonGraceMS:     2500,
		DaemonCores:       8,
		MaxOwnedProcesses: 96,
		OracleTimeout:     750 * time.Millisecond,
	}
}

func SoakConfig(seed int64, d time.Duration) Config {
	return Config{
		Seed:              seed,
		Duration:          d,
		MaxActors:         24,
		Settle:            8 * time.Second,
		EnableCLI:         true,
		FaultBudget:       1.0,
		DaemonIdleMS:      10000,
		DaemonGraceMS:     3000,
		DaemonCores:       16,
		MaxOwnedProcesses: 192,
		OracleTimeout:     750 * time.Millisecond,
	}
}

type Harness struct {
	cfg      Config
	t        testing.TB
	home     string
	rng      *rand.Rand
	jr       *Journal
	dummyBin string
	sparkBin string

	mu             sync.Mutex
	actors         map[string]*actor
	nextID         int
	ctl            *client.Client
	daemons        map[int]*daemonProcess
	daemonKilledAt time.Time
	verSeq         int
	stateReader    func(context.Context) (wingwire.QueueState, error)
	processReader  func() ([]procgroup.Info, error)
	processFailure func([]string)
	guardInterval  time.Duration
	guardFailure   sync.Once
}

type daemonProcess struct {
	group      *procgroup.Group
	done       chan struct{}
	finalizeMu sync.Mutex
	complete   bool

	cleanupFailed bool
}

type actor struct {
	runID    string
	cmd      *exec.Cmd
	cores    float64
	sems     []string
	wedged   bool
	granted  bool
	rejected bool
	killed   bool
	exited   bool

	cleanupFailed bool
	group         *procgroup.Group
	stdout        io.ReadCloser
	scanned       chan struct{}
	finishMu      sync.Mutex
	killedAt      time.Time
	exitedAt      time.Time
}

func Run(t testing.TB, cfg Config) {
	if err := procgroup.Supported(); err != nil {
		t.Fatalf("chaos process ownership: %v", err)
	}
	if cfg.Seed == 0 {
		cfg.Seed = time.Now().UnixNano()
	}
	home, err := os.MkdirTemp("/tmp", "chaos")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() || os.Getenv("SPARKWING_CHAOS_KEEP") != "" {
			t.Logf("chaos home kept for inspection: %s", home)
			return
		}
		_ = os.RemoveAll(home)
	})

	jpath := filepath.Join(home, "journal.jsonl")
	jr, err := NewJournal(jpath)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}

	h := &Harness{
		cfg:     cfg,
		t:       t,
		home:    home,
		rng:     rand.New(rand.NewSource(cfg.Seed)),
		jr:      jr,
		actors:  map[string]*actor{},
		daemons: map[int]*daemonProcess{},
	}
	defer func() { _ = jr.Close() }()
	defer h.cleanup()
	stopGuard := h.startProcessGuard()
	defer stopGuard()
	t.Logf("chaos seed=%d journal=%s home=%s", cfg.Seed, jpath, home)
	jr.Append(Event{Kind: "seed", Detail: strconv.FormatInt(cfg.Seed, 10)})

	h.buildBinaries()
	h.loop()
	stopGuard()
	h.quiesce()
	if t.Failed() {
		return
	}
	h.converge()

	if !t.Failed() {
		t.Logf("chaos passed seed=%d", cfg.Seed)
	}
}

func (h *Harness) buildBinaries() {
	h.dummyBin = h.build("github.com/sparkwing-dev/sparkwing/internal/chaos/crashdummy", true)
	if h.cfg.EnableCLI {
		h.sparkBin = h.build("github.com/sparkwing-dev/sparkwing/cmd/sparkwing", false)
	}
}

func (h *Harness) build(pkg string, required bool) string {
	bin := filepath.Join(h.home, filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if required {
			h.t.Fatalf("build %s: %v", pkg, err)
		}
		h.t.Logf("build %s failed, disabling that injector: %v", pkg, err)
		return ""
	}
	return bin
}

func (h *Harness) loop() {
	deadline := time.Now().Add(h.cfg.Duration)
	lastOS := time.Now()
	for time.Now().Before(deadline) {
		if h.t.Failed() {
			return
		}
		h.step()
		h.checkLedger()
		if h.t.Failed() {
			return
		}
		if time.Since(lastOS) > 300*time.Millisecond {
			h.checkOS()
			h.scanDaemonPanic()
			lastOS = time.Now()
		}
		if h.t.Failed() {
			return
		}
		time.Sleep(time.Duration(15+h.rng.Intn(70)) * time.Millisecond)
	}
}

func (h *Harness) step() {
	fb := h.cfg.FaultBudget
	choices := []struct {
		w  float64
		fn func()
	}{
		{2.5, h.spawnActor},
		{0.8 * fb, h.spawnWedged},
		{1.4 * fb, h.killHolder},
		{1.0 * fb, h.killWaiter},
		{0.5 * fb, h.killDaemon},
		{0.7 * fb, h.takeover},
		{0.8 * fb, h.churn},
		{0.8 * fb, h.malformed},
		{0.6 * fb, h.hammerCLI},
	}
	var total float64
	for _, c := range choices {
		total += c.w
	}
	pick := h.rng.Float64() * total
	for _, c := range choices {
		if pick < c.w {
			c.fn()
			return
		}
		pick -= c.w
	}
}

func (h *Harness) spawnActor() { h.spawn(false) }

func (h *Harness) spawnWedged() { h.spawn(true) }

func (h *Harness) spawn(wedged bool) {
	h.mu.Lock()
	if h.liveCountLocked() >= h.cfg.MaxActors {
		h.mu.Unlock()
		return
	}
	h.nextID++
	runID := "r" + strconv.Itoa(h.nextID)
	h.mu.Unlock()

	args := []string{
		"hold", "--home", h.home, "--run", runID, "--version", "v1.0.0",
		"--daemon-idle-ms", strconv.Itoa(h.cfg.DaemonIdleMS),
		"--daemon-grace-ms", strconv.Itoa(h.cfg.DaemonGraceMS),
		"--daemon-total-cores", strconv.FormatFloat(h.cfg.DaemonCores, 'f', -1, 64),
	}
	a := &actor{runID: runID}

	if wedged {
		args = append(args, "--cores", "0.2", "--ignore-term")
		a.wedged = true
		a.cores = 0.2
	} else if h.rng.Intn(2) == 0 {
		key := []string{"lockA", "lockB", "deploy"}[h.rng.Intn(3)]
		capv := 1 + h.rng.Intn(3)
		pol := weightedPolicy(h.rng)
		sem := fmt.Sprintf("%s:%d:1:%s", key, capv, pol)
		args = append(args, "--cores", "0.1", "--sem", sem)
		a.cores = 0.1
		a.sems = []string{key}
	} else {
		cores := []float64{0.1, 0.25, 0.5, 1, 2}[h.rng.Intn(5)]
		args = append(args, "--cores", strconv.FormatFloat(cores, 'f', -1, 64))
		a.cores = cores
	}
	if !wedged {
		if h.rng.Intn(3) == 0 {
			args = append(args, "--run-ms", strconv.Itoa(200+h.rng.Intn(1400)))
		}
		if h.rng.Intn(4) == 0 {
			args = append(args, "--burn")
		}
		if h.rng.Intn(5) == 0 {
			args = append(args, "--dirty")
		}
	}

	cmd := exec.Command(h.dummyBin, args...)
	cmd.Stderr = nil
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		return
	}
	a.cmd = cmd
	a.group = group
	a.stdout = stdout
	a.scanned = make(chan struct{})
	h.mu.Lock()
	h.actors[runID] = a
	h.mu.Unlock()
	h.jr.Append(Event{Kind: "spawn", Run: runID, Detail: strings.Join(args[3:], " ")})
	go h.watchActor(a, stdout)
}

func startActorCommand(cmd *exec.Cmd) (io.ReadCloser, *procgroup.Group, error) {
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stdout = childStdout
	group, err := procgroup.Start(cmd)
	if err != nil {
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, nil, err
	}
	_ = childStdout.Close()
	return stdout, group, nil
}

func (h *Harness) watchActor(a *actor, stdout io.ReadCloser) {
	scanned := a.scanned
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "OK "):
				h.mu.Lock()
				a.granted = true
				h.mu.Unlock()
				h.jr.Append(Event{Kind: "grant", Run: a.runID})
			case strings.HasPrefix(line, "REJECT"):
				h.mu.Lock()
				a.rejected = true
				h.mu.Unlock()
				h.jr.Append(Event{Kind: "reject", Run: a.runID, Detail: strings.TrimPrefix(line, "REJECT ")})
			}
		}
	}()
	<-a.group.LeaderExited()
	if err := h.finishActor(a, false); err != nil {
		h.t.Errorf("reap actor group %d: %v", a.group.ID(), err)
	}
}

var errActorDrain = errors.New("output stream did not drain within the cleanup bound")

func (h *Harness) finishActor(a *actor, force bool) error {
	a.finishMu.Lock()
	defer a.finishMu.Unlock()
	h.mu.Lock()
	if a.exited {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), h.processWait())
	defer cancel()
	var err error
	if force {
		err = a.group.Terminate(ctx, 100*time.Millisecond)
	} else {
		err = a.group.Finish(ctx, 100*time.Millisecond)
	}
	if errors.Is(err, procgroup.ErrCleanup) {
		h.mu.Lock()
		a.cleanupFailed = true
		h.mu.Unlock()
		return err
	}
	select {
	case <-a.scanned:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errActorDrain, ctx.Err())
	}
	_ = a.stdout.Close()
	h.mu.Lock()
	a.cleanupFailed = false
	a.exited = true
	a.exitedAt = time.Now()
	h.mu.Unlock()
	h.jr.Append(Event{Kind: "exit", Run: a.runID})
	return nil
}

func (h *Harness) killHolder() {
	if a := h.pick(func(a *actor) bool { return a.granted && !a.wedged }); a != nil {
		h.killActor(a, "kill_holder")
	}
}

func (h *Harness) killWaiter() {
	if a := h.pick(func(a *actor) bool { return !a.granted && !a.rejected }); a != nil {
		h.killActor(a, "kill_waiter")
	}
}

func (h *Harness) killActor(a *actor, kind string) {
	h.mu.Lock()
	if a.killed || a.exited || a.cmd == nil || a.cmd.Process == nil {
		h.mu.Unlock()
		return
	}
	a.killed = true
	a.killedAt = time.Now()
	group := a.group
	h.mu.Unlock()
	_ = group.Kill()
	h.jr.Append(Event{Kind: kind, Run: a.runID})
}

func (h *Harness) killDaemon() {
	pid := h.currentDaemonPid()
	if pid <= 0 {
		return
	}
	h.mu.Lock()
	h.daemonKilledAt = time.Now()
	if h.ctl != nil {
		_ = h.ctl.Close()
		h.ctl = nil
	}
	h.mu.Unlock()
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
	h.jr.Append(Event{Kind: "kill_daemon", Detail: strconv.Itoa(pid)})
}

func (h *Harness) takeover() {
	h.mu.Lock()
	if h.liveCountLocked() >= h.cfg.MaxActors {
		h.mu.Unlock()
		return
	}
	h.verSeq++
	ver := fmt.Sprintf("v1.0.%d", h.verSeq)
	h.nextID++
	runID := "t" + strconv.Itoa(h.nextID)
	h.daemonKilledAt = time.Now()
	h.mu.Unlock()

	args := []string{
		"hold", "--home", h.home, "--run", runID, "--version", ver,
		"--cores", "0.2", "--run-ms", strconv.Itoa(600 + h.rng.Intn(800)),
		"--daemon-idle-ms", strconv.Itoa(h.cfg.DaemonIdleMS),
		"--daemon-grace-ms", strconv.Itoa(h.cfg.DaemonGraceMS),
		"--daemon-total-cores", strconv.FormatFloat(h.cfg.DaemonCores, 'f', -1, 64),
	}
	cmd := exec.Command(h.dummyBin, args...)
	stdout, group, err := startActorCommand(cmd)
	if err != nil {
		return
	}
	a := &actor{runID: runID, cores: 0.2, cmd: cmd, group: group, stdout: stdout, scanned: make(chan struct{})}
	h.mu.Lock()
	h.actors[runID] = a
	h.mu.Unlock()
	h.jr.Append(Event{Kind: "takeover", Run: runID, Detail: ver})
	go h.watchActor(a, stdout)
}

func (h *Harness) churn() {
	sock := h.sockPath()
	n := 3 + h.rng.Intn(6)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dialUnix(sock, 200*time.Millisecond)
			if err == nil {
				_ = c.Close()
			}
		}()
	}
	wg.Wait()
	h.jr.Append(Event{Kind: "churn", Detail: strconv.Itoa(n)})
}

func (h *Harness) malformed() {
	sock := h.sockPath()
	c, err := dialUnix(sock, 200*time.Millisecond)
	if err != nil {
		return
	}
	junk := [][]byte{
		[]byte("this is not json\n"),
		[]byte("{\"type\":\"bogus\"}\n"),
		[]byte("{not even valid"),
		[]byte("\x00\x01\x02\x03\n"),
	}
	_, _ = c.Write(junk[h.rng.Intn(len(junk))])
	_ = c.Close()
	h.jr.Append(Event{Kind: "malformed"})
}

func (h *Harness) hammerCLI() {
	if h.sparkBin == "" {
		return
	}
	verbs := [][]string{{"runs", "list", "--limit", "5"}, {"info"}, {"queue"}}
	verb := verbs[h.rng.Intn(len(verbs))]
	cmd := exec.Command(h.sparkBin, verb...)
	cmd.Env = append(os.Environ(), "SPARKWING_HOME="+h.home)
	_ = cmd.Run()
	h.jr.Append(Event{Kind: "cli", Detail: strings.Join(verb, " ")})
}

func (h *Harness) checkLedger() {
	qs, err := h.readState()
	if err != nil {
		return
	}
	if v := checkLedgerTruth(qs); len(v) > 0 {
		h.fail("ledger-truth", v, qs)
	}
	// safety: gate the liveness check on daemon stability -- right after a
	// kill the successor is still restoring holders and re-admitting
	// reconnecting waiters, a transient in which zero holders alongside a
	// waiter is legitimate rather than a stranded run.
	if h.leakStable() && h.daemonGraceStable(qs) {
		if v := checkLivenessTruth(qs); len(v) > 0 {
			h.fail("liveness", v, qs)
		}
	}
}

func (h *Harness) checkOS() {
	qs, err := h.readState()
	if err != nil {
		return
	}
	live, known := h.processSets()
	if v := checkOSTruth(qs, live, known, h.leakStable() && h.daemonGraceStable(qs)); len(v) > 0 {
		h.fail("os-truth", v, qs)
	}
}

func (h *Harness) daemonGraceStable(qs wingwire.QueueState) bool {
	grace := time.Duration(h.cfg.DaemonGraceMS) * time.Millisecond
	return daemonGraceStable(qs, grace, h.cfg.Settle)
}

func (h *Harness) startProcessGuard() func() {
	interval := h.guardInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	guard := newProcessGuard(h)
	stop := make(chan struct{})
	done := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			guard.check()
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		stopOnce.Do(func() {
			close(stop)
			<-done
		})
	}
}

type ownedGroup struct {
	cleanupFailed bool
}

type zombie struct {
	pid   int
	group int
}

type processGuard struct {
	h     *Harness
	since map[zombie]time.Time
}

func newProcessGuard(h *Harness) *processGuard {
	return &processGuard{h: h, since: map[zombie]time.Time{}}
}

func (g *processGuard) check() {
	h := g.h
	groups := h.ownedProcessGroups()
	if len(groups) == 0 {
		clear(g.since)
		return
	}
	processReader := h.processReader
	if processReader == nil {
		processReader = procgroup.List
	}
	processes, err := processReader()
	if err != nil {
		h.reportProcessGrowth([]string{fmt.Sprintf("inspect owned process groups: %v", err)})
		return
	}

	now := time.Now()
	owned := 0
	present := make(map[zombie]bool)
	for _, process := range processes {
		if _, ok := groups[process.Group]; !ok {
			continue
		}
		owned++
		if !strings.HasPrefix(process.State, "Z") {
			continue
		}
		found := zombie{pid: process.PID, group: process.Group}
		present[found] = true
		if _, seen := g.since[found]; !seen {
			g.since[found] = now
		}
	}
	for found := range g.since {
		if !present[found] {
			delete(g.since, found)
		}
	}

	var violations []string
	if limit := h.processLimit(); owned > limit {
		violations = append(violations, fmt.Sprintf("owned processes %d exceed limit %d", owned, limit))
	}
	violations = append(violations, g.stalledZombies(groups, now)...)
	if len(violations) > 0 {
		h.reportProcessGrowth(violations)
	}
}

func (g *processGuard) stalledZombies(groups map[int]ownedGroup, now time.Time) []string {
	bound := g.h.zombieDrain()
	stalled := make([]zombie, 0, len(g.since))
	for found, since := range g.since {
		if groups[found.group].cleanupFailed || now.Sub(since) <= bound {
			continue
		}
		stalled = append(stalled, found)
	}
	slices.SortFunc(stalled, func(a, b zombie) int { return cmp.Compare(a.pid, b.pid) })
	violations := make([]string, 0, len(stalled))
	for _, found := range stalled {
		violations = append(violations, fmt.Sprintf("%s stayed unreaped for %s, exceeding %s",
			found, now.Sub(g.since[found]).Round(time.Millisecond), bound))
	}
	return violations
}

func (z zombie) String() string {
	if z.pid == z.group {
		return fmt.Sprintf("owned group %d leader anchor (pid %d)", z.group, z.pid)
	}
	return fmt.Sprintf("descendant pid %d in owned group %d", z.pid, z.group)
}

func (h *Harness) reportProcessGrowth(violations []string) {
	h.guardFailure.Do(func() {
		if h.processFailure != nil {
			h.processFailure(violations)
			return
		}
		h.fail("process-growth", violations, wingwire.QueueState{})
	})
}

func (h *Harness) processLimit() int {
	if h.cfg.MaxOwnedProcesses > 0 {
		return h.cfg.MaxOwnedProcesses
	}
	return h.cfg.MaxActors*4 + 32
}

func (h *Harness) zombieDrain() time.Duration {
	if h.cfg.MaxZombieDrain > 0 {
		return h.cfg.MaxZombieDrain
	}
	return max(2*h.processWait(), 2*time.Second)
}

func (h *Harness) ownedProcessGroups() map[int]ownedGroup {
	h.mu.Lock()
	defer h.mu.Unlock()
	groups := make(map[int]ownedGroup, len(h.actors)+len(h.daemons))
	for _, a := range h.actors {
		if !a.exited && a.group != nil && !a.group.Reaped() {
			groups[a.group.ID()] = ownedGroup{cleanupFailed: a.cleanupFailed}
		}
	}
	for _, daemon := range h.daemons {
		if daemon.group != nil && !daemon.group.Reaped() {
			groups[daemon.group.ID()] = ownedGroup{cleanupFailed: daemon.cleanupFailed}
		}
	}
	return groups
}

func (h *Harness) leakStable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.daemonKilledAt.IsZero() {
		return true
	}
	grace := time.Duration(h.cfg.DaemonGraceMS) * time.Millisecond
	return time.Since(h.daemonKilledAt) > grace+h.cfg.Settle
}

func (h *Harness) processSets() (live, known map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	live = map[string]bool{}
	known = map[string]bool{}
	for id, a := range h.actors {
		known[id] = true
		settling := a.exited && time.Since(a.exitedAt) < h.cfg.Settle
		if !a.exited || settling {
			live[id] = true
		}
	}
	return live, known
}

func (h *Harness) pick(match func(*actor) bool) *actor {
	h.mu.Lock()
	defer h.mu.Unlock()
	var candidates []*actor
	for _, a := range h.actors {
		if a.exited || a.killed {
			continue
		}
		if match(a) {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates[h.rng.Intn(len(candidates))]
}

func (h *Harness) liveCountLocked() int {
	n := 0
	for _, a := range h.actors {
		if !a.exited {
			n++
		}
	}
	return n
}

func (h *Harness) quiesce() {
	h.jr.Log("quiesce", "", "stopping injection")
	h.mu.Lock()
	var pending []*actor
	for _, a := range h.actors {
		if !a.exited {
			pending = append(pending, a)
			if !a.killed && a.cmd != nil && a.cmd.Process != nil {
				a.killed = true
				a.killedAt = time.Now()
				_ = a.group.Kill()
			}
		}
	}
	h.mu.Unlock()
	deadline := time.Now().Add(h.cfg.Settle + 3*time.Second)
	for time.Now().Before(deadline) {
		if h.allExited(pending) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !h.allExited(pending) {
		h.fail("process-cleanup", []string{"actors did not exit within the quiescence bound"}, wingwire.QueueState{})
	}
}

func (h *Harness) cleanup() {
	h.mu.Lock()
	if h.ctl != nil {
		_ = h.ctl.Close()
		h.ctl = nil
	}
	actors := make([]*actor, 0, len(h.actors))
	for _, a := range h.actors {
		if !a.exited {
			actors = append(actors, a)
		}
	}
	daemons := make([]*daemonProcess, 0, len(h.daemons))
	for _, daemon := range h.daemons {
		daemons = append(daemons, daemon)
	}
	h.mu.Unlock()

	for _, a := range actors {
		if err := h.cleanupActor(a); err != nil {
			h.t.Error(err)
		}
	}
	for _, daemon := range daemons {
		if err := h.cleanupDaemon(daemon); err != nil {
			h.t.Error(err)
		}
	}
}

func (h *Harness) cleanupActor(a *actor) error {
	err := h.finishActor(a, true)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errActorDrain):
		return fmt.Errorf("actor group %d exited and was reaped, but its %w", a.group.ID(), err)
	default:
		return fmt.Errorf("actor process group %d did not exit within cleanup bound: %w", a.group.ID(), err)
	}
}

func (h *Harness) cleanupDaemon(daemon *daemonProcess) error {
	if err := h.finishDaemon(daemon, true); err != nil {
		return fmt.Errorf("daemon process group %d did not exit within cleanup bound: %w", daemon.group.ID(), err)
	}
	return nil
}

func (h *Harness) processWait() time.Duration {
	if h.cfg.Settle > 0 {
		return h.cfg.Settle
	}
	return time.Second
}

func (h *Harness) allExited(as []*actor) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range as {
		if !a.exited {
			return false
		}
	}
	return true
}

func (h *Harness) converge() {
	deadline := time.Now().Add(h.cfg.Settle + 5*time.Second)
	var last []string
	for time.Now().Before(deadline) {
		qs, err := h.readState()
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		last = checkConverged(qs)
		if len(last) == 0 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if len(last) > 0 {
		h.fail("convergence", last, wingwire.QueueState{})
		return
	}
	h.jr.Log("converged", "", "zero leases, zero waiters")

	h.mu.Lock()
	if h.ctl != nil {
		_ = h.ctl.Close()
		h.ctl = nil
	}
	h.mu.Unlock()

	idleWait := time.Duration(h.cfg.DaemonIdleMS)*time.Millisecond + time.Second
	for attempt := 0; attempt < 4; attempt++ {
		time.Sleep(idleWait)
		ctx, cancel := context.WithTimeout(context.Background(), h.oracleTimeout())
		_, err := client.Query(ctx, h.readOpts())
		cancel()
		if errors.Is(err, client.ErrNoDaemon) {
			h.jr.Log("daemon_idle", "", "daemon exited after quiescence")
			return
		}
	}
	h.t.Errorf("daemon did not idle-exit after convergence (seed=%d journal=%s)", h.cfg.Seed, h.jr.Path())
}

func (h *Harness) readState() (wingwire.QueueState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), h.oracleTimeout())
	defer cancel()
	if h.stateReader != nil {
		return h.stateReader(ctx)
	}
	h.mu.Lock()
	cl := h.ctl
	h.mu.Unlock()
	if cl != nil {
		if qs, err := cl.QueueState(ctx); err == nil {
			return qs, nil
		}
		h.mu.Lock()
		if h.ctl == cl {
			_ = h.ctl.Close()
			h.ctl = nil
		}
		h.mu.Unlock()
	}
	cl, err := client.EnsureDaemon(ctx, h.readOpts())
	if err != nil {
		return wingwire.QueueState{}, err
	}
	h.mu.Lock()
	h.ctl = cl
	h.mu.Unlock()
	return cl.QueueState(ctx)
}

func (h *Harness) oracleTimeout() time.Duration {
	if h.cfg.OracleTimeout > 0 {
		return h.cfg.OracleTimeout
	}
	return 750 * time.Millisecond
}

func (h *Harness) readOpts() client.Options {
	return client.Options{
		Home:        h.home,
		Version:     "",
		Spawn:       h.daemonSpawn(),
		DialTimeout: 500 * time.Millisecond,
		Backoff:     30 * time.Millisecond,
	}
}

func (h *Harness) daemonSpawn() func(home, version string) error {
	return func(home, version string) error {
		v := version
		if v == "" {
			v = "v1.0.0"
		}
		cmd := exec.Command(h.dummyBin, "daemon",
			"--home", home,
			"--version", v,
			"--grace-ms", strconv.Itoa(h.cfg.DaemonGraceMS),
			"--idle-ms", strconv.Itoa(h.cfg.DaemonIdleMS),
			"--total-cores", strconv.FormatFloat(h.cfg.DaemonCores, 'f', -1, 64),
		)
		return h.startDaemonCommand(cmd)
	}
}

func (h *Harness) startDaemonCommand(cmd *exec.Cmd) error {
	group, err := procgroup.Start(cmd)
	if err != nil {
		return err
	}
	process := &daemonProcess{group: group, done: make(chan struct{})}
	h.mu.Lock()
	if h.daemons == nil {
		h.daemons = map[int]*daemonProcess{}
	}
	h.daemons[group.ID()] = process
	h.mu.Unlock()
	go func() {
		<-group.LeaderExited()
		if err := h.finishDaemon(process, false); err != nil {
			h.t.Errorf("reap daemon group %d: %v", group.ID(), err)
		}
	}()
	return nil
}

func (h *Harness) finishDaemon(process *daemonProcess, force bool) error {
	process.finalizeMu.Lock()
	defer process.finalizeMu.Unlock()
	if process.complete {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.processWait())
	defer cancel()
	var err error
	if force {
		err = process.group.Terminate(ctx, 100*time.Millisecond)
	} else {
		err = process.group.Finish(ctx, 100*time.Millisecond)
	}
	if errors.Is(err, procgroup.ErrCleanup) {
		h.mu.Lock()
		process.cleanupFailed = true
		h.mu.Unlock()
		return err
	}
	process.complete = true
	h.mu.Lock()
	process.cleanupFailed = false
	delete(h.daemons, process.group.ID())
	h.mu.Unlock()
	close(process.done)
	return nil
}

func (h *Harness) sockPath() string {
	sock, err := wingd.SocketPath(h.home)
	if err != nil {
		return filepath.Join(h.home, "wingd", "d.sock")
	}
	return sock
}

func (h *Harness) currentDaemonPid() int {
	data, err := os.ReadFile(filepath.Join(h.home, "wingd", "daemons.log"))
	if err != nil {
		return -1
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) == 0 {
		return -1
	}
	pid, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return -1
	}
	return pid
}

func (h *Harness) scanDaemonPanic() {
	data, err := os.ReadFile(filepath.Join(h.home, "wingd", "d.log"))
	if err != nil {
		return
	}
	s := string(data)
	if strings.Contains(s, "invariant violated") || strings.Contains(s, "panic:") {
		h.fail("daemon-panic", []string{firstPanicLine(s)}, wingwire.QueueState{})
	}
}

func firstPanicLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "invariant violated") || strings.Contains(line, "panic:") {
			return line
		}
	}
	return "panic in daemon log"
}

func (h *Harness) fail(oracle string, violations []string, qs wingwire.QueueState) {
	h.jr.Append(Event{Kind: "VIOLATION", Detail: oracle, Fields: map[string]any{
		"violations": violations,
		"queue":      qs,
	}})
	h.t.Errorf("CHAOS VIOLATION [%s] seed=%d journal=%s\n  - %s",
		oracle, h.cfg.Seed, h.jr.Path(), strings.Join(violations, "\n  - "))
}

func dialUnix(sock string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.Dial("unix", sock)
}

func weightedPolicy(rng *rand.Rand) wingwire.Policy {
	switch r := rng.Float64(); {
	case r < 0.6:
		return wingwire.PolicyQueue
	case r < 0.75:
		return wingwire.PolicyFail
	case r < 0.85:
		return wingwire.PolicySkip
	default:
		return wingwire.PolicyCancelOthers
	}
}
