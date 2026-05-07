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

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// runDebug dispatches `sparkwing debug <verb>`. 's interactive
// debugging surface lives entirely under this namespace; wing stays
// free of any pause / attach / release flag so production runs cannot
// accidentally carry a debug directive.
func runDebug(args []string) error {
	if handleParentHelp(cmdDebug, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDebug, os.Stderr)
		return errors.New("debug: subcommand required")
	}
	switch args[0] {
	case "run":
		return runDebugRun(args[1:])
	case "release":
		return runDebugRelease(args[1:])
	case "attach":
		return runDebugAttach(args[1:])
	case "env":
		return runDebugEnv(args[1:])
	case "rerun":
		return runDebugRerun(args[1:])
	case "replay":
		return runDebugReplay(args[1:])
	default:
		PrintHelp(cmdDebug, os.Stderr)
		return fmt.Errorf("debug: unknown subcommand %q", args[0])
	}
}

// runDebugRun parses debug-owned flags, converts them to env vars the
// pipeline binary reads in orchestrator.Main, and then forwards the
// pipeline name + remaining args through the normal wing code path.
// Reusing runWing guarantees --from / --config / --on still work
// alongside --pause-*.
func runDebugRun(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		PrintHelp(cmdDebugRun, os.Stdout)
		return nil
	}

	// Hand-parse debug-owned flags (--pipeline plus the three pause
	// directives) and forward everything else to the pipeline binary.
	// Rule: no positional args on sparkwing -- --pipeline names the
	// pipeline to run under debug supervision.
	var pauseBefore, pauseAfter []string
	var pauseOnFailure bool
	pipelineName := ""
	remaining := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--pipeline":
			if i+1 >= len(args) {
				return errors.New("--pipeline requires a value")
			}
			pipelineName = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--pipeline="):
			pipelineName = strings.TrimPrefix(a, "--pipeline=")
			i++
		case a == "--pause-before":
			if i+1 >= len(args) {
				return errors.New("--pause-before requires a node id")
			}
			pauseBefore = append(pauseBefore, args[i+1])
			i += 2
		case strings.HasPrefix(a, "--pause-before="):
			pauseBefore = append(pauseBefore, strings.TrimPrefix(a, "--pause-before="))
			i++
		case a == "--pause-after":
			if i+1 >= len(args) {
				return errors.New("--pause-after requires a node id")
			}
			pauseAfter = append(pauseAfter, args[i+1])
			i += 2
		case strings.HasPrefix(a, "--pause-after="):
			pauseAfter = append(pauseAfter, strings.TrimPrefix(a, "--pause-after="))
			i++
		case a == "--pause-on-failure", a == "--pause-on-failure=true":
			pauseOnFailure = true
			i++
		case a == "--pause-on-failure=false":
			pauseOnFailure = false
			i++
		default:
			remaining = append(remaining, a)
			i++
		}
	}
	if pipelineName == "" {
		PrintHelp(cmdDebugRun, os.Stderr)
		return errors.New("debug run: --pipeline is required")
	}
	if len(pauseBefore) == 0 && len(pauseAfter) == 0 && !pauseOnFailure {
		return errors.New("debug run: need at least one of --pause-before / --pause-after / --pause-on-failure (use `wing` for unpaused runs)")
	}

	if len(pauseBefore) > 0 {
		_ = os.Setenv("SPARKWING_DEBUG_PAUSE_BEFORE", strings.Join(pauseBefore, ","))
	}
	if len(pauseAfter) > 0 {
		_ = os.Setenv("SPARKWING_DEBUG_PAUSE_AFTER", strings.Join(pauseAfter, ","))
	}
	if pauseOnFailure {
		_ = os.Setenv("SPARKWING_DEBUG_PAUSE_ON_FAILURE", "1")
	}

	fmt.Fprintln(os.Stderr, "debug: starting pipeline with pause directives")
	if len(pauseBefore) > 0 {
		fmt.Fprintf(os.Stderr, "  pause-before: %s\n", strings.Join(pauseBefore, ", "))
	}
	if len(pauseAfter) > 0 {
		fmt.Fprintf(os.Stderr, "  pause-after:  %s\n", strings.Join(pauseAfter, ", "))
	}
	if pauseOnFailure {
		fmt.Fprintln(os.Stderr, "  pause-on-failure: true")
	}
	fmt.Fprintln(os.Stderr, "  release with: sparkwing debug release --run <id> --node <name>")

	return runWing(append([]string{pipelineName}, remaining...))
}

// debugTargetFlags are the shared --run/--node/--on parse surface
// for release/attach/env. Pulling this into a helper keeps the three
// CLI entry points terse.
type debugTargetFlags struct {
	run  string
	node string
	on   string
}

func parseDebugTarget(cmd Command, args []string) (debugTargetFlags, error) {
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	nodeID := fs.String("node", "", "node id")
	on := fs.String("on", "", "profile name (cluster mode)")
	if err := parseAndCheck(cmd, fs, args); err != nil {
		return debugTargetFlags{}, err
	}
	if *runID == "" || *nodeID == "" {
		return debugTargetFlags{}, fmt.Errorf("%s: --run and --node are required", cmd.Path)
	}
	return debugTargetFlags{run: *runID, node: *nodeID, on: *on}, nil
}

// runDebugRelease writes the released row. Local mode opens the
// SQLite store directly; cluster mode talks to the controller via
// POST /api/v1/runs/{id}/nodes/{node}/release.
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
		if err := requireController(prof, "debug release"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
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
	defer st.Close()
	if err := st.ReleaseDebugPause(ctx, t.run, t.node, releasedBy, store.PauseReleaseManual); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no active pause for %s/%s (already released or never paused)", t.run, t.node)
		}
		return fmt.Errorf("release %s/%s: %w", t.run, t.node, err)
	}
	fmt.Fprintf(os.Stdout, "released %s/%s\n", t.run, t.node)
	return nil
}

// runDebugAttach shells out to `kubectl exec -it` against the pod
// holding the node's claim. claimed_by is conventionally
// "pod:<runID>:<nodeID>" for K8s Jobs, "runner:<host>" for warm
// runners, and "agent:<host>" for laptop agents. We only know the
// pod name for the pod: and runner: forms running in-cluster; the
// agent case surfaces a clear "no pod to attach to" message.
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
			"attach does not apply in local mode; the node's process runs in your current shell's world.")
		fmt.Fprintln(os.Stdout,
			"Use `sparkwing debug env` to inspect, or `sparkwing debug release` to resume.")
		return nil
	}

	prof, err := resolveProfile(t.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "debug attach"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	node, err := c.GetNode(ctx, t.run, t.node)
	if err != nil {
		return fmt.Errorf("get node %s/%s: %w", t.run, t.node, err)
	}
	if node.ClaimedBy == "" {
		return fmt.Errorf("node %s/%s has no claim holder (not currently running on a runner)",
			t.run, t.node)
	}
	pod, ns := claimToPod(node.ClaimedBy)
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

// runDebugEnv prints env vars + workdir + claim holder for a node.
// Reads straight from the node row (env snapshot comes from the
// plan snapshot; claim holder is the pod / runner owning the lease).
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
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		node, err = c.GetNode(ctx, t.run, t.node)
		if err != nil {
			return fmt.Errorf("get node: %w", err)
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
		defer st.Close()
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
	fmt.Fprintf(w, "run:    %s\nnode:   %s\nstatus: %s\n", t.run, t.node, node.Status)
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

// claimToPod parses a claim-holder string into (pod, namespace). The
// wire format varies by runner type:
func claimToPod(claim string) (pod, namespace string) {
	namespace = os.Getenv("SPARKWING_NAMESPACE")
	if namespace == "" {
		namespace = "sparkwing"
	}
	switch {
	case strings.HasPrefix(claim, "runner:"):
		return strings.TrimPrefix(claim, "runner:"), namespace
	case strings.HasPrefix(claim, "pod:"):
		// pod:<runID>:<nodeID> -- the actual pod name is derived from
		// the Job spec and isn't directly recoverable from the claim
		// string. Fall back to the raw runID:nodeID value; operators
		// can adjust via kubectl get pods + the printed namespace.
		return strings.TrimPrefix(claim, "pod:"), namespace
	}
	return "", namespace
}

// whoami returns the OS user for released_by audit, falling back to
// "cli" when the user lookup fails (CI sandboxes, etc.).
func whoami() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "cli"
}
