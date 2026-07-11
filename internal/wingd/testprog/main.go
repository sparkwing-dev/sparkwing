// Command testprog is a wingd test fixture: a standalone binary that can
// run the daemon or a lease-holding client in a real OS process, so tests
// can exercise election across processes and connection liveness under
// SIGKILL. It is not part of the shipped CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: testprog <daemon|hold|reattach> [flags]")
	}
	switch os.Args[1] {
	case "daemon":
		runDaemon(os.Args[2:])
	case "wingd":
		runWingdMode(os.Args[2:])
	case "hold":
		runHold(os.Args[2:])
	case "reattach":
		runReattach(os.Args[2:])
	default:
		fail("unknown mode %q", os.Args[1])
	}
}

// runWingdMode serves the `wingd run` subcommand the client's default
// spawn re-execs, mirroring the shipped daemon's log wiring: operational
// lines go to stderr, which the spawn points at the daemon log file.
func runWingdMode(args []string) {
	if len(args) == 0 || args[0] != "run" {
		fail("usage: wingd run [--home DIR]")
	}
	fs := flag.NewFlagSet("wingd run", flag.ExitOnError)
	home := fs.String("home", "", "")
	version := fs.String("version", "v1.0.0", "")
	idleMS := fs.Int("idle-ms", 800, "")
	_ = fs.Parse(args[1:])

	d, err := wingd.New(wingd.Config{
		Home:        *home,
		Version:     *version,
		IdleTimeout: time.Duration(*idleMS) * time.Millisecond,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "%s wingd: %s\n",
				time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
		},
	})
	if err != nil {
		fail("new daemon: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && err != wingd.ErrNotElected {
		fail("run: %v", err)
	}
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	home := fs.String("home", "", "")
	version := fs.String("version", "v1.0.0", "")
	graceMS := fs.Int("grace-ms", 3000, "")
	idleMS := fs.Int("idle-ms", 30000, "")
	_ = fs.Parse(args)

	d, err := wingd.New(wingd.Config{
		Home:        *home,
		Version:     *version,
		GraceWindow: time.Duration(*graceMS) * time.Millisecond,
		IdleTimeout: time.Duration(*idleMS) * time.Millisecond,
	})
	if err != nil {
		fail("new daemon: %v", err)
	}
	go func() {
		<-d.Ready()
		recordWin(*home)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && err != wingd.ErrNotElected {
		fail("run: %v", err)
	}
}

// recordWin appends this process's pid to a log the election test reads
// to prove exactly one daemon ever served.
func recordWin(home string) {
	dir, err := wingd.StateDir(home)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "daemons.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, "%d\n", os.Getpid())
}

func runHold(args []string) {
	fs := flag.NewFlagSet("hold", flag.ExitOnError)
	home := fs.String("home", "", "")
	run := fs.String("run", "run", "")
	sem := fs.String("sem", "", "")
	policy := fs.String("sem-policy", "queue", "")
	cancelTimeoutMS := fs.Int64("cancel-timeout-ms", 0, "")
	cores := fs.Float64("cores", 0, "")
	graceMS := fs.Int("daemon-grace-ms", 3000, "")
	idleMS := fs.Int("daemon-idle-ms", 30000, "")
	semaphoresOnly := fs.Bool("semaphores-only", false, "")
	realSpawn := fs.Bool("real-spawn", false, "")
	_ = fs.Parse(args)

	opts := client.Options{
		Home:    *home,
		Spawn:   daemonSpawner(*graceMS, *idleMS),
		Backoff: 30 * time.Millisecond,
	}
	if *realSpawn {
		opts.Spawn = nil
	}
	cl, err := client.EnsureDaemon(context.Background(), opts)
	if err != nil {
		fail("ensure daemon: %v", err)
	}

	req := wingwire.AdmissionRequest{RunID: *run}
	if *sem != "" {
		req.Semaphores = []wingwire.SemaphoreClaim{{
			Name: *sem, Capacity: 1, Cost: 1,
			Policy:          wingwire.Policy(*policy),
			CancelTimeoutMS: *cancelTimeoutMS,
		}}
		if *semaphoresOnly {
			req.SemaphoresOnly = true
		} else {
			req.Resources = wingwire.HostResources{Cores: 0.1}
		}
	} else {
		req.Resources = wingwire.HostResources{Cores: *cores}
	}
	lease, err := cl.Acquire(context.Background(), req, nil)
	if err != nil {
		fail("acquire: %v", err)
	}
	announce(lease.Token)

	evicted := make(chan wingwire.Evicted, 1)
	go lease.Watch(func(ev wingwire.Evicted) { evicted <- ev })
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	select {
	case ev := <-evicted:
		fmt.Printf("EVICTED %s %s\n", ev.Key, ev.SupersededBy)
		_ = os.Stdout.Sync()
	case <-ch:
	}
}

func runReattach(args []string) {
	fs := flag.NewFlagSet("reattach", flag.ExitOnError)
	home := fs.String("home", "", "")
	token := fs.String("token", "", "")
	graceMS := fs.Int("daemon-grace-ms", 3000, "")
	idleMS := fs.Int("daemon-idle-ms", 30000, "")
	_ = fs.Parse(args)

	opts := client.Options{
		Home:    *home,
		Spawn:   daemonSpawner(*graceMS, *idleMS),
		Backoff: 30 * time.Millisecond,
	}
	cl, err := client.EnsureDaemon(context.Background(), opts)
	if err != nil {
		fail("ensure daemon: %v", err)
	}
	lease, err := cl.Reattach(context.Background(), *token)
	if err != nil {
		fail("reattach: %v", err)
	}
	announce(lease.Token)
	block()
}

// daemonSpawner returns a Spawn hook that re-execs this fixture as a
// detached daemon.
func daemonSpawner(graceMS, idleMS int) func(home, version string) error {
	return func(home, version string) error {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(self, "daemon",
			"--home", home,
			"--grace-ms", strconv.Itoa(graceMS),
			"--idle-ms", strconv.Itoa(idleMS),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}

func announce(token string) {
	fmt.Printf("OK %s\n", token)
	_ = os.Stdout.Sync()
}

func block() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
