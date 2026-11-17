package isolationprobe

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Exec runs a command to completion and returns its exit code. err reports a command that could
// not start or was stopped; nothing the command printed reaches a result.
type Exec func(ctx context.Context, name string, args ...string) (int, error)

const probeBinary = "isolation-probe"

// safety: every daemon call carries its own deadline and cleanup runs on a fresh bounded context,
// so an unresponsive daemon or CLI cannot hold the probe past them.
const (
	dockerCallTimeout    = 30 * time.Second
	dockerImportTimeout  = 5 * time.Minute
	dockerRunTimeout     = 8 * time.Minute
	dockerCleanupTimeout = 30 * time.Second
)

// safety: 125 is the docker CLI's own code for a daemon that refused to create or start the
// container, distinct from any code the container itself exits with.
const dockerRefused = 125

type dockerCLI struct {
	exec Exec
	host string
}

func (d dockerCLI) run(ctx context.Context, timeout time.Duration, args ...string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.exec(cctx, "docker", append([]string{"--host", d.host}, args...)...)
}

func (d dockerCLI) cleanup(ctx context.Context, args ...string) {
	_, _ = d.run(context.WithoutCancel(ctx), dockerCleanupTimeout, args...) //nolint:errcheck // best-effort removal of a throwaway object
}

// safety: outcomes name only the CLI's exit code or why it did not finish, never daemon text.
func cliOutcome(code int, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case err != nil:
		return "did not run"
	default:
		return "exit " + strconv.Itoa(code)
	}
}

type dindRun struct {
	name    string
	configs [][]string
}

// safety: the privileged container asks for everything the sidecar could grant; when dockerd
// refuses that, the refusal is recorded and smaller sets down to the sidecar's own privileges
// still run the checks.
func dindRuns(exchange string) []dindRun {
	out := "--volume=" + exchange + ":/out"
	host := "--volume=/:/host:ro"
	unconfined := []string{"--security-opt=seccomp=unconfined", "--security-opt=apparmor=unconfined"}
	return []dindRun{
		{name: ContextBridge, configs: [][]string{{out, "--network=bridge"}}},
		{name: ContextPrivileged, configs: [][]string{
			append([]string{out, host, "--privileged", "--pid=host", "--network=host", "--ipc=host", "--uts=host", "--cgroupns=host", "--cap-add=ALL"}, unconfined...),
			{out, host, "--privileged", "--pid=host", "--network=host"},
			append([]string{out, host, "--pid=host", "--network=host", "--cap-add=SYS_ADMIN", "--cap-add=NET_ADMIN", "--cap-add=SYS_PTRACE"}, unconfined...),
		}},
	}
}

// safety: the image holds only the probe's own static executable, imported from a tarball, so no
// builder runs, nothing is pulled from a registry and the containers run exactly the code tested.
func dindContexts(ctx context.Context, d dockerCLI, runDir string, plan Plan, netns string, own []string, executable []byte) []Result {
	fail := func(outcome string) []Result {
		return []Result{{Item: "C12", Context: ContextPrivileged, Probe: "probe image", Expect: ExpectOpen, Outcome: outcome, Verdict: Inconclusive}}
	}
	daemon := Result{Item: "C9", Context: ContextJob, Probe: probeOwnDocker, Target: "docker.sock", Expect: ExpectOpen, Outcome: "answered", Verdict: Pass}
	if code, err := d.run(ctx, dockerCallTimeout, "version"); err != nil || code != 0 {
		daemon.Outcome, daemon.Verdict = "no answer: "+cliOutcome(code, err), Inconclusive
		return []Result{daemon}
	}
	out := []Result{daemon}
	tarball := filepath.Join(runDir, "image.tar")
	exchange := filepath.Join(runDir, "exchange")
	if outcome := stageImage(executable, tarball, exchange, plan); outcome != "" {
		return append(out, fail(outcome)...)
	}
	tag := probeBinary + ":" + randomHex(6)
	if code, err := d.run(ctx, dockerImportTimeout, "import", tarball, tag); err != nil || code != 0 {
		return append(out, fail("import failed: "+cliOutcome(code, err))...)
	}
	defer d.cleanup(ctx, "rmi", "--force", tag)
	runs := dindRuns(exchange)
	results := make([][]Result, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runContext(ctx, d, tag, exchange, netns, own, run)
		}()
	}
	wg.Wait()
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

// safety: Docker resolves bind sources in the sidecar, which mounts the same scratch volume, so
// the exchange directory beside the run's other files is visible to both; the containers' root
// may be another uid, so the directory is writable by all.
func stageImage(executable []byte, tarball, exchange string, plan Plan) string {
	if ok, err := static(executable); err != nil || !ok {
		return "executable is not a static ELF"
	}
	image, err := imageTar(executable)
	if err != nil {
		return "image tarball unwritable"
	}
	// #nosec G703 -- tarball is a fixed name inside the probe's own run directory.
	if err := os.WriteFile(tarball, image, 0o600); err != nil {
		return "image tarball unwritable"
	}
	if err := os.MkdirAll(exchange, 0o700); err != nil {
		return "exchange directory unusable"
	}
	if err := os.Chmod(exchange, 0o777); err != nil {
		return "exchange directory unusable"
	}
	planData, err := json.Marshal(plan)
	if err == nil {
		err = os.WriteFile(filepath.Join(exchange, "plan.json"), planData, 0o644)
	}
	if err != nil {
		return "exchange plan unwritable"
	}
	return ""
}

func imageTar(executable []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: probeBinary, Mode: 0o755, Size: int64(len(executable)), ModTime: time.Unix(0, 0)}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(executable); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func runContext(ctx context.Context, d dockerCLI, tag, exchange, netns string, own []string, run dindRun) []Result {
	outFile := run.name + ".ndjson"
	cmd := []string{
		"/" + probeBinary, Command, "context", "--context", run.name, "--plan", "/out/plan.json", "--out", "/out/" + outFile,
		"--pod-netns", netns, "--work-dir", "/out/work-" + run.name, "--own", strings.Join(own, ","),
	}
	var notes []Result
	for i, flags := range run.configs {
		name := probeBinary + "-" + run.name + "-" + randomHex(4)
		args := append(append([]string{"run", "--rm", "--name", name}, flags...), tag)
		code, err := d.run(ctx, dockerRunTimeout, append(args, cmd...)...)
		if err != nil {
			d.cleanup(ctx, "rm", "--force", name)
		}
		if err == nil && code == dockerRefused {
			if i < len(run.configs)-1 {
				notes = append(notes, Result{Item: "C12", Context: run.name, Probe: "dockerd grants every requested privilege", Expect: ExpectObserved, Outcome: "refused: " + cliOutcome(code, nil), Verdict: Observed})
				continue
			}
			return append(notes, Result{Item: "C12", Context: run.name, Probe: "container starts", Expect: ExpectOpen, Outcome: "refused: " + cliOutcome(code, nil), Verdict: Inconclusive})
		}
		return append(notes, collect(exchange, outFile, run.name, code, err)...)
	}
	return notes
}

// safety: a context's results count only when its container exited cleanly and every line names
// that context, so a crashed or confused container fails the run instead of passing on part of it.
func collect(exchange, outFile, where string, code int, err error) []Result {
	data, rerr := os.ReadFile(filepath.Join(exchange, outFile))
	results, perr := parseResultFile(data, where)
	if err != nil || code != 0 || rerr != nil || perr != nil || len(results) == 0 {
		return []Result{{Item: "C12", Context: where, Probe: "container reports results", Expect: ExpectOpen, Outcome: "no results: " + cliOutcome(code, err), Verdict: Inconclusive}}
	}
	return results
}

func parseResultFile(data []byte, where string) ([]Result, error) {
	var out []Result
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		if r.Context != where {
			return nil, fmt.Errorf("a %s result arrived from the %s container", r.Context, where)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// safety: a FROM scratch image has no dynamic loader, so only an executable without PT_INTERP can
// run in it.
func static(executable []byte) (bool, error) {
	f, err := elf.NewFile(bytes.NewReader(executable))
	if err != nil {
		return false, err
	}
	defer f.Close() //nolint:errcheck // an in-memory reader
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false, nil
		}
	}
	return true, nil
}
