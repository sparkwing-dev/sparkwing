package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// rerunFlags is rerun's parse surface. parseDebugTarget doesn't fit
// because rerun also accepts --seq and (for cluster mode) --image.
type rerunFlags struct {
	run   string
	node  string
	on    string
	seq   int
	image string
}

func parseRerunFlags(args []string) (rerunFlags, error) {
	fs := flag.NewFlagSet(cmdDebugRerun.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	nodeID := fs.String("node", "", "node id")
	on := fs.String("on", "", "profile name (cluster mode)")
	seq := fs.Int("seq", -1, "attempt index; -1 selects most recent")
	image := fs.String("image", "", "runner image for cluster-mode debug pod (overrides $SPARKWING_RERUN_IMAGE)")
	if err := parseAndCheck(cmdDebugRerun, fs, args); err != nil {
		return rerunFlags{}, err
	}
	if *runID == "" || *nodeID == "" {
		return rerunFlags{}, fmt.Errorf("%s: --run and --node are required", cmdDebugRerun.Path)
	}
	return rerunFlags{
		run: *runID, node: *nodeID, on: *on,
		seq: *seq, image: *image,
	}, nil
}

// runDebugRerun reproduces the dispatch frame for one (run, node, seq)
// and drops the operator into a shell. Local mode exec's $SHELL with
// the snapshot env applied; cluster mode shells out to `kubectl run`
// against a runner image so the operator gets a pod that mirrors the
// dispatch environment.
func runDebugRerun(args []string) error {
	t, err := parseRerunFlags(args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx := context.Background()

	if t.on != "" {
		return runDebugRerunCluster(ctx, t)
	}
	return runDebugRerunLocal(ctx, t)
}

// runDebugRerunLocal opens the local store, fetches the dispatch
// snapshot, materializes upstream Ref outputs to a scratch dir, and
// exec's the user's shell with the snapshot env applied.
func runDebugRerunLocal(ctx context.Context, t rerunFlags) error {
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()

	snap, err := st.GetNodeDispatch(ctx, t.run, t.node, t.seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no dispatch snapshot for %s/%s (seq=%d) -- run may predate the dispatch-snapshot feature",
				t.run, t.node, t.seq)
		}
		return fmt.Errorf("get dispatch: %w", err)
	}
	node, err := st.GetNode(ctx, t.run, t.node)
	if err != nil {
		return fmt.Errorf("get node row: %w", err)
	}

	refsDir := filepath.Join(paths.Root, "rerun", t.run, t.node, "refs")
	if err := materializeLocalRefs(ctx, st, refsDir, t.run, node.Deps); err != nil {
		return fmt.Errorf("materialize refs: %w", err)
	}

	envList, err := BuildRerunEnv(snap, refsDir, os.Environ())
	if err != nil {
		return fmt.Errorf("build rerun env: %w", err)
	}

	printRerunBanner(os.Stderr, snap, node, refsDir)

	shell := pickShell()
	workdir := snap.Workdir
	if workdir == "" {
		workdir = "."
	}
	if err := os.Chdir(workdir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cd %s failed (%v); shell will start in %s\n",
			workdir, err, mustGetwd())
	}
	return syscall.Exec(shell, []string{shell}, envList)
}

// runDebugRerunCluster builds and exec's a `kubectl run` command with
// the snapshot env materialized as --env=K=V flags. The image comes
// from --image > $SPARKWING_RERUN_IMAGE; absence is a hard error so
// the operator knows what to set.
func runDebugRerunCluster(ctx context.Context, t rerunFlags) error {
	prof, err := resolveProfile(t.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "debug rerun"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)

	snap, err := c.GetNodeDispatch(ctx, t.run, t.node, t.seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no dispatch snapshot for %s/%s (seq=%d) on %s",
				t.run, t.node, t.seq, prof.Name)
		}
		return fmt.Errorf("get dispatch: %w", err)
	}
	node, err := c.GetNode(ctx, t.run, t.node)
	if err != nil {
		return fmt.Errorf("get node row: %w", err)
	}

	image := t.image
	if image == "" {
		image = os.Getenv("SPARKWING_RERUN_IMAGE")
	}
	if image == "" {
		return errors.New("cluster-mode rerun needs an image: pass --image or set SPARKWING_RERUN_IMAGE")
	}

	envMap, err := decodeSnapshotEnv(snap.EnvJSON)
	if err != nil {
		return fmt.Errorf("decode snapshot env: %w", err)
	}
	// Add the rerun marker but skip the refs scratch dir -- we'd need
	// to copy outputs into the pod for that to work, which is
	// out-of-scope for v1. Operators who need ref bodies in cluster
	// mode can `sparkwing jobs status` then re-fetch from the
	// controller via curl from inside the pod.
	envMap["SPARKWING_RERUN"] = "1"

	pod := podName(t.run, t.node)
	args := []string{"kubectl", "run", pod, "--rm", "-it", "--restart=Never",
		"--image=" + image,
		"--labels=sparkwing.dev/rerun-of-run=" + t.run + ",sparkwing.dev/managed-by=sparkwing-debug",
	}
	for _, k := range sortedKeys(envMap) {
		args = append(args, "--env="+k+"="+envMap[k])
	}
	args = append(args, "--command", "--", "/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh")

	printRerunBanner(os.Stderr, snap, node, "")
	fmt.Fprintln(os.Stderr, strings.Join(args, " "))
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found in PATH: %w", err)
	}
	return syscall.Exec(bin, args, os.Environ())
}

// BuildRerunEnv overlays the dispatch snapshot env on top of base so
// SPARKWING_* keys win without losing the operator's PATH / shell
// integrations. Adds SPARKWING_RERUN=1 and points
// SPARKWING_RERUN_REFS_DIR at refsDir when non-empty. Exported so the
// PR 2 tests + future debug-env consumer can call through it.
func BuildRerunEnv(snap *store.NodeDispatch, refsDir string, base []string) ([]string, error) {
	merged := map[string]string{}
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		merged[kv[:i]] = kv[i+1:]
	}
	snapEnv, err := decodeSnapshotEnv(snap.EnvJSON)
	if err != nil {
		return nil, err
	}
	for k, v := range snapEnv {
		merged[k] = v
	}
	merged["SPARKWING_RERUN"] = "1"
	if refsDir != "" {
		merged["SPARKWING_RERUN_REFS_DIR"] = refsDir
	}

	out := make([]string, 0, len(merged))
	for _, k := range sortedKeys(merged) {
		out = append(out, k+"="+merged[k])
	}
	return out, nil
}

func decodeSnapshotEnv(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// materializeLocalRefs writes each upstream node's output_json under
// refsDir/<dep_node_id>.json. Called by local rerun so the operator
// can grep / cat upstream outputs without re-running them.
func materializeLocalRefs(ctx context.Context, st *store.Store, refsDir, runID string, deps []string) error {
	if len(deps) == 0 {
		return nil
	}
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		return err
	}
	for _, dep := range deps {
		n, err := st.GetNode(ctx, runID, dep)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// A dep that no longer exists is a soft warning, not
				// a hard fail -- the operator may still want the shell.
				fmt.Fprintf(os.Stderr, "warning: dep %s not found, skipping ref file\n", dep)
				continue
			}
			return err
		}
		body := n.Output
		if len(body) == 0 {
			body = []byte("null")
		}
		if err := os.WriteFile(filepath.Join(refsDir, dep+".json"), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func printRerunBanner(w io.Writer, snap *store.NodeDispatch, node *store.Node, refsDir string) {
	fmt.Fprintln(w, "── sparkwing debug rerun ────────────────────────────────")
	fmt.Fprintf(w, "  run:      %s\n", snap.RunID)
	fmt.Fprintf(w, "  node:     %s (seq=%d)\n", snap.NodeID, snap.Seq)
	if !snap.DispatchedAt.IsZero() {
		fmt.Fprintf(w, "  captured: %s\n", snap.DispatchedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if snap.CodeVersion != "" {
		fmt.Fprintf(w, "  code:     %s\n", snap.CodeVersion)
	}
	if snap.Workdir != "" {
		fmt.Fprintf(w, "  workdir:  %s\n", snap.Workdir)
	}
	if refsDir != "" {
		fmt.Fprintf(w, "  refs:     %s\n", refsDir)
	}
	if node != nil {
		if node.Status != "" {
			fmt.Fprintf(w, "  status:   %s\n", node.Status)
		}
		if node.Error != "" {
			fmt.Fprintf(w, "  error:    %s\n", trimSingleLine(node.Error, 120))
		}
		if node.FailureReason != "" {
			fmt.Fprintf(w, "  reason:   %s\n", node.FailureReason)
		}
	}
	fmt.Fprintln(w, "  exit shell to release. SPARKWING_RERUN=1 set so any wing/sparkwing")
	fmt.Fprintln(w, "  invocations in this shell can detect the rerun frame.")
	fmt.Fprintln(w, "─────────────────────────────────────────────────────────")
}

// pickShell picks the shell to exec for local rerun. Honors $SHELL
// when valid, else /bin/bash, else /bin/sh.
func pickShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// podName produces a DNS-label-safe pod name for cluster rerun.
// Format: sparkwing-rerun-<6-hex>. We don't try to encode run/node in
// the name -- pod labels carry the lineage, the name just needs to be
// unique enough across concurrent debugs.
func podName(runID, nodeID string) string {
	var buf [3]byte
	_, _ = rand.Read(buf[:])
	return "sparkwing-rerun-" + hex.EncodeToString(buf[:])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func trimSingleLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
