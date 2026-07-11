// Command crashdummy is the chaos harness's synthetic run process. It is
// a test-only binary the harness builds on the fly and drives as a real
// OS process so admission liveness is exercised through the kernel:
// SIGKILL a crashdummy and the daemon learns of the death by the socket
// closing, exactly as it would for a real run.
//
// It has two modes. `daemon` elects and serves a [wingd.Daemon] for an
// isolated sparkwing home with a fixed host sampler, so admission
// capacity is deterministic and machine-independent. `hold` connects,
// submits one admission request, announces its lease token on stdout, and
// then behaves per its flags: run for a bounded time or forever, burn CPU
// or sit idle (a wedged holder), hold memory, exit clean or dirty, ignore
// SIGTERM so only SIGKILL ends it, spawn children that attach to its
// lease, and re-attach to a successor daemon after a daemon kill or
// version takeover.
//
// The binary is not part of the shipped CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

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

// fixedSampler reports a constant host capacity so admission gating on
// cores and memory is deterministic regardless of the real machine.
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
	graceMS := fs.Int("grace-ms", 2000, "")
	idleMS := fs.Int("idle-ms", 4000, "")
	totalCores := fs.Float64("total-cores", 8, "")
	totalMemMB := fs.Int("total-mem-mb", 8192, "")
	_ = fs.Parse(args)

	d, err := wingd.New(wingd.Config{
		Home:             *home,
		Version:          *version,
		GraceWindow:      time.Duration(*graceMS) * time.Millisecond,
		IdleTimeout:      time.Duration(*idleMS) * time.Millisecond,
		HeadroomFraction: -1,
		Sampler:          fixedSampler{cores: *totalCores, mem: uint64(*totalMemMB) << 20},
		FinalizeRun:      finalizeLogger(*home),
	})
	if err != nil {
		fail("new daemon: %v", err)
	}
	go func() {
		<-d.Ready()
		recordPid(*home)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && err != wingd.ErrNotElected {
		fail("run: %v", err)
	}
}

// finalizeLogger appends each finalized run id to finalized.log so the
// harness can assert that a killed holder's run row was reconciled rather
// than left hanging.
func finalizeLogger(home string) func(string) {
	return func(runID string) {
		appendLine(home, "finalized.log", runID)
	}
}

func recordPid(home string) { appendLine(home, "daemons.log", strconv.Itoa(os.Getpid())) }

func appendLine(home, name, line string) {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return
	}
	path := dirOf(sock) + "/" + name
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintln(f, line)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
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
	parentToken string
	children    int
	daemonGrace int
	daemonIdle  int
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
	fs.StringVar(&hf.parentToken, "parent-token", "", "")
	fs.IntVar(&hf.children, "children", 0, "")
	fs.IntVar(&hf.daemonGrace, "daemon-grace-ms", 2000, "")
	fs.IntVar(&hf.daemonIdle, "daemon-idle-ms", 4000, "")
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

	h.spawnChildren(lease.Token)
	if h.hf.burn {
		go burnCPU(h.hf.cores)
	}
	var ballast [][]byte
	if h.hf.memMB > 0 {
		ballast = holdMemory(h.hf.memMB)
	}
	_ = ballast

	h.installSignals()
	if h.hf.runMS > 0 {
		go func() {
			time.Sleep(time.Duration(h.hf.runMS) * time.Millisecond)
			h.cleanExit()
		}()
	}
	h.holdLoop()
}

func (h *holder) request() wingwire.AdmissionRequest {
	if h.hf.parentToken != "" {
		return wingwire.AdmissionRequest{RunID: h.hf.run, ParentLeaseToken: h.hf.parentToken}
	}
	req := wingwire.AdmissionRequest{
		RunID:     h.hf.run,
		Resources: wingwire.HostResources{Cores: h.hf.cores, MemoryBytes: int64(h.hf.memMB) << 20},
	}
	for _, s := range h.hf.sems {
		if c, ok := parseSem(s); ok {
			req.Semaphores = append(req.Semaphores, c)
		}
	}
	return req
}

// holdLoop is the lease's lifetime: it watches the connection, exits on
// eviction, and re-attaches to a successor daemon when the connection
// drops from a daemon kill or version takeover. It returns only when the
// process is exiting.
func (h *holder) holdLoop() {
	for {
		evicted := false
		h.current().Watch(func(wingwire.Evicted) { evicted = true })
		select {
		case <-h.done:
			return
		default:
		}
		if evicted {
			os.Exit(3)
		}
		if !h.reattach() {
			os.Exit(4)
		}
	}
}

// reattach reclaims the lease from a freshly elected daemon within the
// grace window, spawning the successor if none is up yet. It reports
// whether the lease was recovered.
func (h *holder) reattach() bool {
	deadline := time.Now().Add(time.Duration(h.hf.daemonGrace)*time.Millisecond + 2*time.Second)
	token := h.current().Token
	for time.Now().Before(deadline) {
		select {
		case <-h.done:
			return true
		default:
		}
		cl, err := client.EnsureDaemon(context.Background(), client.Options{
			Home:    h.hf.home,
			Version: h.hf.version,
			Spawn:   h.daemonSpawner(),
			Backoff: 25 * time.Millisecond,
		})
		if err != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		lease, rerr := cl.Reattach(context.Background(), token)
		if rerr == nil {
			h.set(cl, lease)
			return true
		}
		_ = cl.Close()
		if rerr == client.ErrReattachRejected {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
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

func (h *holder) spawnChildren(token string) {
	childMS := h.hf.runMS
	if childMS <= 0 {
		childMS = 3000
	}
	for i := 0; i < h.hf.children; i++ {
		self, err := os.Executable()
		if err != nil {
			return
		}
		cmd := exec.Command(self, "hold",
			"--home", h.hf.home,
			"--run", h.hf.run+"-c"+strconv.Itoa(i),
			"--parent-token", token,
			"--version", h.hf.version,
			"--run-ms", strconv.Itoa(childMS),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
		}
	}
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
		cmd := exec.Command(self, "daemon",
			"--home", home,
			"--version", v,
			"--grace-ms", strconv.Itoa(h.hf.daemonGrace),
			"--idle-ms", strconv.Itoa(h.hf.daemonIdle),
			"--total-cores", strconv.FormatFloat(h.hf.daemonCores, 'f', -1, 64),
			"--total-mem-mb", strconv.Itoa(h.hf.daemonMemMB),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
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

// burnCPU spins the requested number of cores until the process exits.
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

// holdMemory allocates and touches roughly mb megabytes so the process's
// real resident set reflects its declared memory cost.
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
