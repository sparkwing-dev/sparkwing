package isolationprobe

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeDocker struct {
	t        *testing.T
	mu       sync.Mutex
	calls    [][]string
	refuse   func(flags []string) bool
	results  map[string][]Result
	exit     map[string]int
	stall    bool
	imported []byte
	cleanups []string
}

func (f *fakeDocker) exec(ctx context.Context, name string, args ...string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != "docker" || len(args) < 3 || args[0] != "--host" || args[1] != "unix:///tmp/docker.sock" {
		f.t.Errorf("unexpected command %s %v", name, args)
		return -1, errors.New("unexpected command")
	}
	if _, ok := ctx.Deadline(); !ok {
		f.t.Errorf("docker %v ran without a deadline", args[2:])
	}
	args = args[2:]
	f.calls = append(f.calls, args)
	switch args[0] {
	case "version":
		return f.exit["version"], nil
	case "import":
		f.imported = f.readImage(args[1])
		return f.exit["import"], nil
	case "rm", "rmi":
		if ctx.Err() != nil {
			f.t.Errorf("cleanup %v ran on a finished context", args)
		}
		f.cleanups = append(f.cleanups, strings.Join(args, " "))
		return 0, nil
	case "run":
		return f.run(ctx, args)
	}
	f.t.Errorf("unexpected docker %v", args)
	return -1, errors.New("unexpected docker call")
}

func (f *fakeDocker) readImage(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Errorf("import tarball: %v", err)
		return nil
	}
	tr := tar.NewReader(bytes.NewReader(data))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != probeBinary || hdr.Mode != 0o755 {
		f.t.Errorf("image entry = %+v, %v", hdr, err)
		return nil
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		f.t.Error(err)
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		f.t.Errorf("image holds more than the probe: %v", err)
	}
	return body
}

func (f *fakeDocker) run(ctx context.Context, args []string) (int, error) {
	image := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, probeBinary+":") })
	flags, cmd := args[1:image], args[image+1:]
	if f.stall {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	if f.refuse != nil && f.refuse(flags) {
		return dockerRefused, nil
	}
	where, out := flagValueIn(cmd, "--context"), flagValueIn(cmd, "--out")
	var exchange string
	for _, fl := range flags {
		if v, ok := strings.CutPrefix(fl, "--volume="); ok && strings.HasSuffix(v, ":/out") {
			exchange = strings.TrimSuffix(v, ":/out")
		}
	}
	if results, ok := f.results[where]; ok {
		var buf bytes.Buffer
		for _, r := range results {
			line, err := json.Marshal(r)
			if err != nil {
				f.t.Fatal(err)
			}
			buf.Write(append(line, '\n'))
		}
		if err := os.WriteFile(filepath.Join(exchange, strings.TrimPrefix(out, "/out/")), buf.Bytes(), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return f.exit[where], nil
}

func flagValueIn(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func (f *fakeDocker) cli() dockerCLI { return dockerCLI{exec: f.exec, host: "unix:///tmp/docker.sock"} }

func (f *fakeDocker) runs() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] == "run" {
			out = append(out, c)
		}
	}
	return out
}

func elfFile(progTypes ...uint32) []byte {
	const ehsize, phentsize = 64, 56
	b := make([]byte, ehsize+phentsize*len(progTypes))
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[32:], ehsize)
	binary.LittleEndian.PutUint16(b[52:], ehsize)
	binary.LittleEndian.PutUint16(b[54:], phentsize)
	binary.LittleEndian.PutUint16(b[56:], uint16(len(progTypes)))
	for i, typ := range progTypes {
		binary.LittleEndian.PutUint32(b[ehsize+i*phentsize:], typ)
	}
	return b
}

const ptLoad, ptInterp = 1, 3

func dindPlan() Plan {
	return Plan{ControllerURL: "https://controller.example.invalid", ListenPort: 18080, Canary: "isolation-canary-a"}
}

var ownForTest = []string{"/tmp/docker.sock", "/tmp/docker", "/tmp/docker-exec", "/tmp/isolation-probe/run-isolation-canary-a"}

// The job ships only its own executable as the image, runs both containers from it with the pod's
// own paths handed on, and removes the image when done.
func TestDindRunsTheProbeImageInBothContextsAndRemovesIt(t *testing.T) {
	bridge := Result{Item: "C4", Context: ContextBridge, Probe: "IMDS", Verdict: Pass}
	privileged := Result{Item: "C12", Context: ContextPrivileged, Probe: "seccomp refuses bpf", Verdict: Pass}
	f := &fakeDocker{t: t, results: map[string][]Result{ContextBridge: {bridge}, ContextPrivileged: {privileged}}}
	exe := elfFile(ptLoad)
	out := dindContexts(t.Context(), f.cli(), t.TempDir(), dindPlan(), "net:[1]", ownForTest, exe)
	if len(out) != 3 || out[0].Probe != probeOwnDocker || out[0].Verdict != Pass || !slices.Contains(out, bridge) || !slices.Contains(out, privileged) {
		t.Fatalf("results = %+v", out)
	}
	if !bytes.Equal(f.imported, exe) {
		t.Fatal("the image does not hold the probe's own executable")
	}
	runs := f.runs()
	if len(runs) != 2 {
		t.Fatalf("runs = %v, want one per context", runs)
	}
	for _, r := range runs {
		joined := strings.Join(r, " ")
		if r[1] != "--rm" || !strings.Contains(joined, " --own "+strings.Join(ownForTest, ",")) || !strings.Contains(joined, "--pod-netns net:[1]") {
			t.Errorf("run = %v", r)
		}
		switch flagValueIn(r, "--context") {
		case ContextBridge:
			if !slices.Contains(r, "--network=bridge") || slices.Contains(r, "--privileged") {
				t.Errorf("bridge run = %v", r)
			}
		case ContextPrivileged:
			for _, want := range []string{"--privileged", "--pid=host", "--network=host", "--cgroupns=host", "--cap-add=ALL", "--volume=/:/host:ro", "--security-opt=seccomp=unconfined"} {
				if !slices.Contains(r, want) {
					t.Errorf("privileged run lacks %s: %v", want, r)
				}
			}
		}
	}
	if len(f.cleanups) != 1 || !strings.HasPrefix(f.cleanups[0], "rmi --force "+probeBinary+":") {
		t.Fatalf("cleanups = %v, want the image removed", f.cleanups)
	}
}

// The privileged container asks for every privilege the sidecar can grant and runs with a smaller
// set when dockerd refuses some of them.
func TestPrivilegedContextFallsBackWhenDockerdRefusesAPrivilege(t *testing.T) {
	exchange := t.TempDir()
	breach := Result{Item: "C12", Context: ContextPrivileged, Probe: "kernel file opens for writing", Expect: ExpectBlocked, Outcome: "opened", Verdict: Breach}
	f := &fakeDocker{t: t, refuse: func(flags []string) bool { return slices.Contains(flags, "--uts=host") }, results: map[string][]Result{ContextPrivileged: {breach}}}
	results := runContext(t.Context(), f.cli(), "isolation-probe:test", exchange, "net:[1]", ownForTest, dindRuns(exchange)[1])
	if len(results) != 2 || results[0].Verdict != Observed || results[0].Outcome != "refused: exit 125" || results[1] != breach {
		t.Fatalf("results = %+v, want the refusal noted and the container's breach", results)
	}
	if second := f.runs()[1]; !slices.Contains(second, "--privileged") || slices.Contains(second, "--uts=host") || !slices.Contains(second, "--volume=/:/host:ro") {
		t.Fatalf("fallback run = %v", second)
	}
}

// When dockerd refuses every privileged set, the checks run with the sidecar's own privileges, and
// a container that never starts is inconclusive.
func TestPrivilegedContextEndsAtTheSidecarsOwnPrivileges(t *testing.T) {
	exchange := t.TempDir()
	ok := Result{Item: "C12", Context: ContextPrivileged, Probe: "seccomp refuses bpf", Expect: ExpectBlocked, Outcome: "EPERM", Verdict: Pass}
	f := &fakeDocker{t: t, refuse: func(flags []string) bool { return slices.Contains(flags, "--privileged") }, results: map[string][]Result{ContextPrivileged: {ok}}}
	results := runContext(t.Context(), f.cli(), "isolation-probe:test", exchange, "net:[1]", ownForTest, dindRuns(exchange)[1])
	if len(results) != 3 || results[0].Verdict != Observed || results[1].Verdict != Observed || results[2] != ok {
		t.Fatalf("results = %+v, want two refusals noted and the container's result", results)
	}
	last := f.runs()[2]
	for _, want := range []string{"--cap-add=SYS_ADMIN", "--cap-add=NET_ADMIN", "--cap-add=SYS_PTRACE", "--pid=host", "--network=host"} {
		if !slices.Contains(last, want) {
			t.Errorf("last-resort run lacks %s: %v", want, last)
		}
	}
	none := &fakeDocker{t: t, refuse: func([]string) bool { return true }}
	results = runContext(t.Context(), none.cli(), "isolation-probe:test", exchange, "net:[1]", ownForTest, dindRuns(exchange)[1])
	if last := results[len(results)-1]; last.Verdict != Inconclusive || last.Probe != "container starts" {
		t.Fatalf("every set refused = %+v, want inconclusive", results)
	}
}

// Outcomes name only exit codes and stalls, and results count only from a clean exit whose every
// line names the container's own context.
func TestDindOutcomesCarryOnlyExitCodes(t *testing.T) {
	exchange := t.TempDir()
	crashed := &fakeDocker{t: t, exit: map[string]int{ContextBridge: 2}}
	if r := runContext(t.Context(), crashed.cli(), "isolation-probe:test", exchange, "net:[1]", nil, dindRuns(exchange)[0]); len(r) != 1 || r[0].Verdict != Inconclusive || r[0].Outcome != "no results: exit 2" {
		t.Fatalf("crashed container = %+v", r)
	}
	confused := &fakeDocker{t: t, results: map[string][]Result{ContextBridge: {{Context: ContextJob, Probe: "IMDS", Verdict: Pass}}}}
	if r := runContext(t.Context(), confused.cli(), "isolation-probe:test", exchange, "net:[1]", nil, dindRuns(exchange)[0]); len(r) != 1 || r[0].Verdict != Inconclusive {
		t.Fatalf("a job result from the bridge container = %+v, want inconclusive", r)
	}
	silent := &fakeDocker{t: t, exit: map[string]int{"version": 1}}
	if r := dindContexts(t.Context(), silent.cli(), t.TempDir(), dindPlan(), "net:[1]", nil, elfFile(ptLoad)); len(r) != 1 || r[0].Outcome != "no answer: exit 1" || r[0].Verdict != Inconclusive {
		t.Fatalf("silent daemon = %+v", r)
	}
	refused := &fakeDocker{t: t, exit: map[string]int{"import": 1}}
	r := dindContexts(t.Context(), refused.cli(), t.TempDir(), dindPlan(), "net:[1]", nil, elfFile(ptLoad))
	if len(r) != 2 || r[1].Outcome != "import failed: exit 1" || r[1].Verdict != Inconclusive {
		t.Fatalf("refused import = %+v", r)
	}
	dynamic := &fakeDocker{t: t}
	r = dindContexts(t.Context(), dynamic.cli(), t.TempDir(), dindPlan(), "net:[1]", nil, elfFile(ptInterp, ptLoad))
	if len(r) != 2 || r[1].Outcome != "executable is not a static ELF" || len(dynamic.runs()) != 0 {
		t.Fatalf("dynamic executable = %+v", r)
	}
}

// A container that never finishes is stopped at its deadline, reported as a stall, and removed on a
// fresh bounded context even after the job's own context ended.
func TestStalledContainerIsBoundedAndRemoved(t *testing.T) {
	exchange := t.TempDir()
	f := &fakeDocker{t: t, stall: true}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := runContext(ctx, f.cli(), "isolation-probe:test", exchange, "net:[1]", nil, dindRuns(exchange)[0])
	if len(r) != 1 || r[0].Verdict != Inconclusive || r[0].Outcome != "no results: did not run" {
		t.Fatalf("stalled container = %+v", r)
	}
	if len(f.cleanups) != 1 || !strings.HasPrefix(f.cleanups[0], "rm --force "+probeBinary+"-"+ContextBridge+"-") {
		t.Fatalf("cleanups = %v, want the container removed", f.cleanups)
	}
	if cliOutcome(-1, context.DeadlineExceeded) != "timed out" {
		t.Fatal("a deadline is not reported as a stall")
	}
}

func TestStaticRefusesAnExecutableThatNeedsALoader(t *testing.T) {
	if ok, err := static(elfFile(ptLoad)); err != nil || !ok {
		t.Fatalf("static ELF = %v, %v", ok, err)
	}
	if ok, err := static(elfFile(ptInterp, ptLoad)); err != nil || ok {
		t.Fatalf("dynamic ELF = %v, %v", ok, err)
	}
	if _, err := static([]byte("#!/bin/sh\n")); err == nil {
		t.Fatal("a script passed as an ELF")
	}
}
