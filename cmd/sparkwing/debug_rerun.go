package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	on := fs.String("profile", "", "profile name (cluster mode)")
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

func runDebugRerun(args []string) error {
	t, err := parseRerunFlags(args)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	// safety: the cluster path must survive a signal long enough to delete the debug pod and the plaintext env it carries.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if t.on != "" {
		return runDebugRerunCluster(ctx, t)
	}
	return runDebugRerunLocal(ctx, t)
}

func runDebugRerunLocal(ctx context.Context, t rerunFlags) error {
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	st, _, done, err := orchestrator.OpenStoreForRunWrite(ctx, paths, t.run, cmdDebugRerun.Path)
	if err != nil {
		return err
	}
	defer done()

	snap, err := st.GetNodeDispatch(ctx, t.run, t.node, t.seq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf(
				"no dispatch snapshot for %s/%s (seq=%d) -- run may predate the dispatch-snapshot feature",
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

func runDebugRerunCluster(ctx context.Context, t rerunFlags) error {
	prof, err := resolveProfile(t.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "debug rerun"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())

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
	if len(envMap) == 0 {
		fmt.Fprintln(os.Stderr, "warning: the controller returned no dispatch env; env_json needs an admin token")
	}
	envMap["SPARKWING_RERUN"] = "1"

	pod := podName(t.run, t.node)
	manifest, err := rerunPodManifest(pod, image, t.run, envMap)
	if err != nil {
		return fmt.Errorf("build pod manifest: %w", err)
	}

	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found in PATH: %w", err)
	}

	printRerunBanner(os.Stderr, snap, node, "")
	fmt.Fprintf(os.Stderr, "kubectl create -f -  # pod/%s, %d env vars on stdin\n", pod, len(envMap))

	create := exec.CommandContext(ctx, bin, "create", "-f", "-")
	create.Stdin = bytes.NewReader(manifest)
	create.Stdout = os.Stderr
	create.Stderr = os.Stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("kubectl create pod/%s: %w", pod, err)
	}
	defer deleteRerunPod(bin, pod)

	attach := exec.CommandContext(ctx, bin, "attach", "--stdin", "--tty",
		"--pod-running-timeout="+rerunAttachTimeout.String(), "pod/"+pod)
	attach.Stdin = os.Stdin
	attach.Stdout = os.Stdout
	attach.Stderr = os.Stderr
	if err := attach.Run(); err != nil {
		return fmt.Errorf("kubectl attach pod/%s: %w", pod, err)
	}
	return nil
}

const rerunAttachTimeout = 5 * time.Minute

const rerunDeleteTimeout = 30 * time.Second

const rerunPodDeadlineSecs = 3600

func rerunPodManifest(pod, image, runID string, env map[string]string) ([]byte, error) {
	vars := make([]map[string]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		vars = append(vars, map[string]string{"name": k, "value": env[k]})
	}
	return json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": pod,
			"labels": map[string]string{
				"sparkwing.dev/rerun-of-run": runID,
				"sparkwing.dev/managed-by":   "sparkwing-debug",
			},
		},
		"spec": map[string]any{
			"restartPolicy":         "Never",
			"activeDeadlineSeconds": rerunPodDeadlineSecs,
			"containers": []map[string]any{{
				"name":      "rerun",
				"image":     image,
				"stdin":     true,
				"stdinOnce": true,
				"tty":       true,
				"command":   []string{"/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"},
				"env":       vars,
			}},
		},
	})
}

func deleteRerunPod(bin, pod string) {
	ctx, cancel := context.WithTimeout(context.Background(), rerunDeleteTimeout)
	defer cancel()
	del := exec.CommandContext(ctx, bin, "delete", "pod", pod, "--ignore-not-found", "--wait=false")
	del.Stdout = os.Stderr
	del.Stderr = os.Stderr
	if err := del.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: pod/%s left behind (%v); delete it with: kubectl delete pod %s\n", pod, err, pod)
	}
}

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

func decodeRedactedKeys(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func materializeLocalRefs(ctx context.Context, st *store.Store, refsDir, runID string, deps []string) error {
	if len(deps) == 0 {
		return nil
	}
	if err := fssecure.EnsureDir(refsDir); err != nil {
		return err
	}
	for _, dep := range deps {
		n, err := st.GetNode(ctx, runID, dep)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fmt.Fprintf(os.Stderr, "warning: dep %s not found, skipping ref file\n", dep)
				continue
			}
			return err
		}
		body := n.Output
		if len(body) == 0 {
			body = []byte("null")
		}
		if err := fssecure.WriteFile(filepath.Join(refsDir, dep+".json"), body); err != nil {
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
	if keys := decodeRedactedKeys(snap.RedactedKeys); len(keys) > 0 {
		fmt.Fprintf(w, "  dropped:  %s\n", strings.Join(keys, " "))
		fmt.Fprintln(w, "            these names or values looked credential-shaped, so the snapshot")
		fmt.Fprintln(w, "            dropped them and replaced URL passwords with \"redacted\";")
		fmt.Fprintln(w, "            export what you need yourself")
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
	fmt.Fprintln(w, "  exit shell to release. SPARKWING_RERUN=1 set so any sparkwing")
	fmt.Fprintln(w, "  invocations in this shell can detect the rerun frame.")
	fmt.Fprintln(w, "─────────────────────────────────────────────────────────")
}

func pickShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		// #nosec G703 -- the shell path comes from this user's own environment
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

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
