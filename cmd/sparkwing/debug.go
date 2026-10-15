package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runDebug(args []string) error {
	if handleParentHelp(cmdDebug, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDebug, os.Stderr)
		return errors.New("debug: subcommand required")
	}
	switch args[0] {
	case "resume":
		return runDebugRelease(args[1:])
	case "attach":
		return runDebugAttach(args[1:])
	case "env":
		return runDebugEnv(args[1:])
	default:
		PrintHelp(cmdDebug, os.Stderr)
		return fmt.Errorf("debug: unknown subcommand %q", args[0])
	}
}

type debugTargetFlags struct {
	run       string
	node      string
	on        string
	namespace string
}

const defaultDebugNamespace = "sparkwing"

func parseDebugTarget(cmd Command, args []string) (debugTargetFlags, error) {
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	nodeID := fs.String("job", "", "job id")
	on := fs.String("profile", "", "controller profile name")
	namespace := defaultDebugNamespace
	if cmd.Path == cmdDebugAttach.Path {
		fs.StringVar(&namespace, "namespace", defaultDebugNamespace, "Kubernetes namespace of the runner pods")
	}
	if err := parseAndCheck(cmd, fs, args); err != nil {
		return debugTargetFlags{}, err
	}
	if *runID == "" || *nodeID == "" {
		return debugTargetFlags{}, fmt.Errorf("%s: --run and --job are required", cmd.Path)
	}
	return debugTargetFlags{run: *runID, node: *nodeID, on: *on, namespace: namespace}, nil
}

func runDebugRelease(args []string) error {
	t, err := parseDebugTarget(cmdDebugRelease, args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	releasedBy := whoami()
	ctx := context.Background()

	if t.on != "" {
		prof, err := resolveProfile(t.on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "debug resume"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		if err := c.ReleaseDebugPause(ctx, t.run, t.node, releasedBy, store.PauseReleaseManual); err != nil {
			return fmt.Errorf("release %s/%s: %w", t.run, t.node, err)
		}
		fmt.Fprintf(os.Stdout, "released %s/%s on %s\n", t.run, t.node, prof.Name)
		return nil
	}

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.ReleaseDebugPause(ctx, t.run, t.node, releasedBy, store.PauseReleaseManual); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no active pause for %s/%s (already released or never paused)", t.run, t.node)
		}
		return fmt.Errorf("release %s/%s: %w", t.run, t.node, err)
	}
	fmt.Fprintf(os.Stdout, "released %s/%s\n", t.run, t.node)
	return nil
}

func runDebugAttach(args []string) error {
	t, err := parseDebugTarget(cmdDebugAttach, args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx := context.Background()

	if t.on == "" {
		fmt.Fprintln(os.Stdout,
			"attach does not apply in local mode; the job's process runs in your current shell's world.")
		fmt.Fprintln(os.Stdout,
			"Use `sparkwing debug env` to inspect, or `sparkwing debug resume` to resume.")
		return nil
	}

	prof, err := resolveProfile(t.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "debug attach"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	node, err := c.GetNode(ctx, t.run, t.node)
	if err != nil {
		return fmt.Errorf("get job %s/%s: %w", t.run, t.node, err)
	}
	if node.ClaimedBy == "" {
		return fmt.Errorf("job %s/%s has no claim holder (not currently running on a runner)",
			t.run, t.node)
	}
	pod := claimToPod(node.ClaimedBy)
	ns := t.namespace
	if pod == "" {
		return fmt.Errorf("claim holder %q does not map to a cluster pod (agent-owned claim?)",
			node.ClaimedBy)
	}
	fmt.Fprintf(os.Stderr, "kubectl exec -it -n %s %s -- bash\n", ns, pod)
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found in PATH: %w", err)
	}
	return syscall.Exec(bin, []string{"kubectl", "exec", "-it", "-n", ns, pod, "--", "bash"}, os.Environ())
}

func runDebugEnv(args []string) error {
	t, err := parseDebugTarget(cmdDebugEnv, args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx := context.Background()

	var node *store.Node
	var pause *store.DebugPause
	if t.on != "" {
		prof, err := resolveProfile(t.on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "debug env"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		node, err = c.GetNode(ctx, t.run, t.node)
		if err != nil {
			return fmt.Errorf("get job: %w", err)
		}
		p, perr := c.GetActiveDebugPause(ctx, t.run, t.node)
		if perr == nil {
			pause = p
		}
	} else {
		paths, err := orchestrator.DefaultPaths()
		if err != nil {
			return err
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return oerr
		}
		defer func() { _ = st.Close() }()
		node, err = st.GetNode(ctx, t.run, t.node)
		if err != nil {
			return err
		}
		p, perr := st.GetActiveDebugPause(ctx, t.run, t.node)
		if perr == nil {
			pause = p
		}
	}

	w := os.Stdout
	fmt.Fprintf(w, "run:    %s\njob:    %s\nstatus: %s\n", t.run, t.node, node.Status)
	if node.ClaimedBy != "" {
		fmt.Fprintf(w, "claim:  %s\n", node.ClaimedBy)
	}
	if node.Error != "" {
		fmt.Fprintf(w, "error:  %s\n", node.Error)
	}
	if pause != nil {
		fmt.Fprintf(w, "paused: %s (reason=%s, expires=%s)\n",
			pause.PausedAt.Format("15:04:05"), pause.Reason,
			pause.ExpiresAt.Format("15:04:05"))
	} else {
		fmt.Fprintln(w, "paused: no active pause (env info is captured at pause time, not continuously)")
	}
	return nil
}

func claimToPod(claim string) string {
	switch {
	case strings.HasPrefix(claim, "runner:"):
		return strings.TrimPrefix(claim, "runner:")
	case strings.HasPrefix(claim, "pod:"):
		return strings.TrimPrefix(claim, "pod:")
	}
	return ""
}

func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "cli"
}
