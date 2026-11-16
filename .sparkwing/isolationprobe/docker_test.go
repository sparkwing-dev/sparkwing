package isolationprobe

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeDocker struct {
	mu          sync.Mutex
	exchange    string
	refuse      func(hostConfig) bool
	results     map[string]string
	importError string
	importQuery string
	created     []containerConfig
	tarNames    []string
	tarModes    []int64
	removed     []string
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/_ping":
		_, _ = io.WriteString(w, "OK")
	case path == "/images/create":
		f.importQuery = r.URL.RawQuery
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			f.tarNames = append(f.tarNames, h.Name)
			f.tarModes = append(f.tarModes, h.Mode)
		}
		if f.importError != "" {
			_, _ = io.WriteString(w, `{"status":"Downloading"}`+"\n"+`{"error":"`+f.importError+`"}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `{"status":"sha256:0123"}`+"\n")
	case path == "/containers/create":
		var cfg containerConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if f.refuse != nil && f.refuse(cfg.HostConfig) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"invalid UTS mode"}`)
			return
		}
		f.created = append(f.created, cfg)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"Id":"c`+string(rune('0'+len(f.created)))+`"}`)
	case strings.HasSuffix(path, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(path, "/wait"):
		cfg := f.created[len(f.created)-1]
		name := cfg.Cmd[4]
		if body, ok := f.results[name]; ok {
			_ = os.WriteFile(filepath.Join(f.exchange, name+".ndjson"), []byte(body), 0o644)
		}
		_, _ = io.WriteString(w, `{"StatusCode":0}`)
	case strings.HasSuffix(path, "/logs"):
		line := []byte("isolation-probe: plan unreadable\n")
		header := make([]byte, 8)
		header[0] = 2
		binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
		_, _ = w.Write(append(header, line...))
	case r.Method == http.MethodDelete:
		f.removed = append(f.removed, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func serveDocker(t *testing.T, f *fakeDocker) *dockerClient {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return newDocker(sock)
}

func resultLines(results ...Result) string {
	var b strings.Builder
	for _, r := range results {
		data, _ := json.Marshal(r)
		b.Write(append(data, '\n'))
	}
	return b.String()
}

func TestImportShipsOnlyTheProbeExecutable(t *testing.T) {
	f := &fakeDocker{}
	d := serveDocker(t, f)
	if err := d.importImage(t.Context(), probeBinary, "test", []byte("\x7fELF")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.tarNames, ",") != probeBinary || f.tarModes[0] != 0o755 || f.importQuery != "fromSrc=-&repo=isolation-probe&tag=test" {
		t.Fatalf("import = %v %o %q", f.tarNames, f.tarModes, f.importQuery)
	}
	f.importError = "no space left on device"
	if err := d.importImage(t.Context(), probeBinary, "test", []byte("\x7fELF")); err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("a failed import streamed as 200 passed: %v", err)
	}
}

// The privileged container asks for every privilege the sidecar can grant, and runs with a smaller
// privileged set when dockerd refuses some of them.
func TestPrivilegedContextFallsBackWhenDockerdRefusesAPrivilege(t *testing.T) {
	exchange := t.TempDir()
	breach := Result{Item: "C12", Context: ContextPrivileged, Probe: "kernel file opens for writing", Expect: ExpectBlocked, Outcome: "opened", Verdict: Breach}
	f := &fakeDocker{exchange: exchange, refuse: func(h hostConfig) bool { return h.UTSMode == "host" }, results: map[string]string{ContextPrivileged: resultLines(breach)}}
	d := serveDocker(t, f)
	results := runContext(t.Context(), d, "isolation-probe:test", exchange, "net:[4026532001]", dindRuns(exchange)[1])
	if len(results) != 2 || results[0].Verdict != Observed || results[1] != breach {
		t.Fatalf("results = %+v, want the refusal noted and the container's breach", results)
	}
	cfg := f.created[0]
	if !cfg.HostConfig.Privileged || cfg.HostConfig.PidMode != "host" || cfg.HostConfig.NetworkMode != "host" || cfg.HostConfig.Binds[1] != "/:/host:ro" {
		t.Fatalf("fallback config = %+v", cfg.HostConfig)
	}
	if got := strings.Join(cfg.Cmd, " "); !strings.HasPrefix(got, "/isolation-probe "+Command+" context --context dind-privileged") || !strings.Contains(got, "--pod-netns net:[4026532001]") {
		t.Fatalf("container command = %q", got)
	}
	if len(f.removed) != 1 {
		t.Fatalf("removed = %v, want the container removed", f.removed)
	}
}

// When dockerd refuses every privileged set, the checks run with the sidecar's own privileges, and
// each refusal is recorded.
func TestPrivilegedContextEndsAtTheSidecarsOwnPrivileges(t *testing.T) {
	exchange := t.TempDir()
	ok := Result{Item: "C12", Context: ContextPrivileged, Probe: "seccomp refuses bpf", Expect: ExpectBlocked, Outcome: "EPERM", Verdict: Pass}
	f := &fakeDocker{exchange: exchange, refuse: func(h hostConfig) bool { return h.Privileged }, results: map[string]string{ContextPrivileged: resultLines(ok)}}
	d := serveDocker(t, f)
	results := runContext(t.Context(), d, "isolation-probe:test", exchange, "net:[1]", dindRuns(exchange)[1])
	if len(results) != 3 || results[0].Verdict != Observed || results[1].Verdict != Observed || results[2] != ok {
		t.Fatalf("results = %+v, want two refusals noted and the container's result", results)
	}
	h := f.created[0].HostConfig
	if h.Privileged || strings.Join(h.CapAdd, ",") != "SYS_ADMIN,NET_ADMIN,SYS_PTRACE" || h.PidMode != "host" || h.NetworkMode != "host" {
		t.Fatalf("last-resort config = %+v", h)
	}
	none := &fakeDocker{exchange: exchange, refuse: func(hostConfig) bool { return true }}
	results = runContext(t.Context(), serveDocker(t, none), "isolation-probe:test", exchange, "net:[1]", dindRuns(exchange)[1])
	if last := results[len(results)-1]; last.Verdict != Inconclusive || last.Probe != "container starts" {
		t.Fatalf("every config refused = %+v, want inconclusive", results)
	}
}

func TestAContainerWithoutResultsIsInconclusive(t *testing.T) {
	exchange := t.TempDir()
	f := &fakeDocker{exchange: exchange}
	d := serveDocker(t, f)
	results := runContext(t.Context(), d, "isolation-probe:test", exchange, "net:[1]", dindRuns(exchange)[0])
	if len(results) != 1 || results[0].Verdict != Inconclusive || !strings.Contains(results[0].Outcome, "plan unreadable") {
		t.Fatalf("results = %+v, want inconclusive with the container's last log line", results)
	}
	if f.created[0].HostConfig.NetworkMode != "bridge" || f.created[0].HostConfig.Privileged {
		t.Fatalf("bridge context config = %+v", f.created[0].HostConfig)
	}
}

func TestDemuxLogsJoinsFrames(t *testing.T) {
	var buf bytes.Buffer
	for _, s := range []string{"one\n", "two\n"} {
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(s)))
		buf.Write(append(header, s...))
	}
	got, err := demuxLogs(&buf)
	if err != nil || got != "one\ntwo\n" {
		t.Fatalf("demux = %q, %v", got, err)
	}
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

func TestStaticRefusesAnExecutableThatNeedsALoader(t *testing.T) {
	const ptLoad, ptInterp = 1, 3
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

func TestDockerErrorsCarryTheDaemonsMessage(t *testing.T) {
	f := &fakeDocker{refuse: func(hostConfig) bool { return true }}
	d := serveDocker(t, f)
	_, err := d.create(t.Context(), "x", containerConfig{Image: "i"})
	var de *dockerError
	if !errors.As(err, &de) || de.status != http.StatusBadRequest || de.message != "invalid UTS mode" {
		t.Fatalf("create error = %v", err)
	}
}
