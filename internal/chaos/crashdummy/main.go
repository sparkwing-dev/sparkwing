package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: crashdummy <daemon|hold> [flags]")
	}
	switch os.Args[1] {
	case "daemon":
		runDaemon(os.Args[2:])
	case "hold":
		runHold(os.Args[2:])
	default:
		fail("unknown mode %q", os.Args[1])
	}
}

type fixedSampler struct {
	cores float64
	mem   uint64
}

func (s fixedSampler) Sample() (wingd.HostStat, error) {
	return wingd.HostStat{
		TotalCores:       s.cores,
		TotalMemoryBytes: s.mem,
		FreeMemoryBytes:  s.mem,
	}, nil
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	home := fs.String("home", "", "")
	version := fs.String("version", "v1.0.0", "")
	_ = fs.Int("grace-ms", 2000, "oracle settle interval only")
	totalCores := fs.Float64("total-cores", 8, "")
	totalMemMB := fs.Int("total-mem-mb", 8192, "")
	_ = fs.Parse(args)
	boundHome, err := wingd.HomeDir(*home)
	if err != nil {
		fail("home: %v", err)
	}
	state := &admissionStore{path: filepath.Join(boundHome, "state.db")}

	d, err := wingd.New(wingd.Config{
		Home:             boundHome,
		Version:          *version,
		HeadroomFraction: -1,
		Sampler:          fixedSampler{cores: *totalCores, mem: uint64(*totalMemMB) << 20},
		Runs:             finalizeStore(finalizeLogger(boundHome)),
		AdmissionStore:   state,
	})
	if err != nil {
		fail("new daemon: %v", err)
	}
	go func() {
		<-d.Ready()
		recordPid(boundHome)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runErr := d.Run(ctx)
	if errors.Is(runErr, wingd.ErrNotElected) {
		runErr = nil
	}
	if err := errors.Join(runErr, state.close()); err != nil {
		fail("run: %v", err)
	}
}

type finalizeStore func(string)

func (finalizeStore) IsRunTerminal(wingwire.RunID) (bool, error) { return false, nil }
func (finalizeStore) Ready() error                               { return nil }
func (f finalizeStore) FinalizeRun(runID string)                 { f(runID) }
func (f finalizeStore) FinalizeCancelledRuns(runIDs []string, _ string) error {
	for _, runID := range runIDs {
		f(runID)
	}
	return nil
}

func finalizeLogger(home string) func(string) {
	return func(runID string) {
		appendLine(home, "finalized.log", runID)
	}
}

func recordPid(home string) { appendLine(home, "daemons.log", strconv.Itoa(os.Getpid())) }

func appendLine(home, name, line string) {
	dir, err := wingd.StateDir(home)
	if err != nil {
		return
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintln(f, line)
}

type holdFlags struct {
	home        string
	run         string
	version     string
	cores       float64
	memMB       int
	sems        []string
	runMS       int
	burn        bool
	dirty       bool
	ignoreTerm  bool
	daemonCores float64
	daemonMemMB int
}

func runHold(args []string) {
	var hf holdFlags
	fs := flag.NewFlagSet("hold", flag.ExitOnError)
	fs.StringVar(&hf.home, "home", "", "")
	fs.StringVar(&hf.run, "run", "run", "")
	fs.StringVar(&hf.version, "version", "v1.0.0", "")
	fs.Float64Var(&hf.cores, "cores", 0.1, "")
	fs.IntVar(&hf.memMB, "mem-mb", 0, "")
	fs.StringArrayVar(&hf.sems, "sem", nil, "name:capacity:cost:policy")
	fs.IntVar(&hf.runMS, "run-ms", 0, "0 means run until killed")
	fs.BoolVar(&hf.burn, "burn", false, "")
	fs.BoolVar(&hf.dirty, "dirty", false, "")
	fs.BoolVar(&hf.ignoreTerm, "ignore-term", false, "")
	_ = fs.Int("daemon-grace-ms", 2000, "oracle settle interval only")
	fs.Float64Var(&hf.daemonCores, "daemon-total-cores", 8, "")
	fs.IntVar(&hf.daemonMemMB, "daemon-total-mem-mb", 8192, "")
	_ = fs.Parse(args)

	h := &holder{hf: hf}
	h.run()
}

type holder struct {
	hf    holdFlags
	mu    sync.Mutex
	lease *client.Lease
	cl    *client.Client
	done  chan struct{}
}

func (h *holder) run() {
	h.done = make(chan struct{})
	h.installSignals()
	opts := client.Options{
		Home:    h.hf.home,
		Version: h.hf.version,
		Spawn:   h.daemonSpawner(),
		Backoff: 25 * time.Millisecond,
	}
	cl, err := client.EnsureDaemon(context.Background(), opts)
	if err != nil {
		fail("ensure daemon: %v", err)
	}
	req := h.request()
	// hack: chaos-test driver; queue position updates have no run log to write to.
	lease, err := cl.Acquire(context.Background(), req, nil)
	if err != nil {
		var ae *client.AdmissionError
		if asAdmission(err, &ae) {
			fmt.Printf("REJECT %s %s\n", ae.Policy, ae.Key)
			_ = os.Stdout.Sync()
			if ae.Policy == wingwire.PolicySkip {
				os.Exit(0)
			}
			os.Exit(2)
		}
		fail("acquire: %v", err)
	}
	h.set(cl, lease)
	announce(lease.Token)

	if h.hf.burn {
		go burnCPU(h.hf.cores)
	}
	var ballast [][]byte
	if h.hf.memMB > 0 {
		ballast = holdMemory(h.hf.memMB)
	}
	if h.hf.runMS > 0 {
		go func() {
			time.Sleep(time.Duration(h.hf.runMS) * time.Millisecond)
			h.cleanExit()
		}()
	}
	h.holdLoop()
	runtime.KeepAlive(ballast)
}

func (h *holder) request() wingwire.AdmissionRequest {
	req := wingwire.AdmissionRequest{
		RunID:     wingwire.HolderRef(h.hf.run),
		Resources: wingwire.HostResources{Cores: h.hf.cores, MemoryBytes: int64(h.hf.memMB) << 20},
	}
	for _, s := range h.hf.sems {
		if c, ok := parseSem(s); ok {
			req.Semaphores = append(req.Semaphores, c)
		}
	}
	return req
}

func (h *holder) holdLoop() {
	cancelled := false
	err := h.current().WatchControl(func(wingwire.Evicted) { cancelled = true }, func(wingwire.Cancel) { cancelled = true })
	select {
	case <-h.done:
		return
	default:
	}
	if cancelled {
		os.Exit(3)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lease watch: %v\n", err)
		os.Exit(4)
	}
}

func (h *holder) cleanExit() {
	select {
	case <-h.done:
		return
	default:
	}
	close(h.done)
	if h.hf.dirty {
		os.Exit(1)
	}
	if l := h.current(); l != nil {
		_ = l.Release()
	}
	os.Exit(0)
}

func (h *holder) installSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for range ch {
			if h.hf.ignoreTerm {
				continue
			}
			h.cleanExit()
		}
	}()
}

func (h *holder) daemonSpawner() func(home, version string) error {
	return func(home, version string) error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		v := version
		if v == "" {
			v = h.hf.version
		}
		cmd := procenv.ControlCommand(os.Environ(), self, "daemon",
			"--home", home,
			"--version", v,
			"--total-cores", strconv.FormatFloat(h.hf.daemonCores, 'f', -1, 64),
			"--total-mem-mb", strconv.Itoa(h.hf.daemonMemMB),
		)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
}

func (h *holder) set(cl *client.Client, lease *client.Lease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cl, h.lease = cl, lease
}

func (h *holder) current() *client.Lease {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease
}

func burnCPU(cores float64) {
	n := int(cores + 0.5)
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		go func() {
			x := 0
			for {
				x++
				if x < 0 {
					fmt.Fprint(os.Stderr, "")
				}
			}
		}()
	}
}

func holdMemory(mb int) [][]byte {
	const chunk = 1 << 20
	out := make([][]byte, 0, mb)
	for i := 0; i < mb; i++ {
		b := make([]byte, chunk)
		for j := 0; j < chunk; j += 4096 {
			b[j] = byte(j)
		}
		out = append(out, b)
	}
	return out
}

func parseSem(s string) (wingwire.SemaphoreClaim, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 4 {
		return wingwire.SemaphoreClaim{}, false
	}
	capacity, err1 := strconv.Atoi(parts[1])
	cost, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return wingwire.SemaphoreClaim{}, false
	}
	return wingwire.SemaphoreClaim{
		Name:     parts[0],
		Capacity: capacity,
		Cost:     cost,
		Policy:   wingwire.Policy(parts[3]),
	}, true
}

func asAdmission(err error, target **client.AdmissionError) bool {
	ae, ok := err.(*client.AdmissionError)
	if ok {
		*target = ae
	}
	return ok
}

func announce(token string) {
	fmt.Printf("OK %s\n", token)
	_ = os.Stdout.Sync()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
