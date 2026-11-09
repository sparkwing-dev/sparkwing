package acceptance_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestArtifactsCLIRealMembersAndInterrupt(t *testing.T) {
	cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY")
	if cli == "" {
		t.Skip("set SPARKWING_LOCAL_E2E_BINARY to a CLI built from this worktree")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	controller := filepath.Join(t.TempDir(), "controller")
	build := procenv.ControlCommandContext(t.Context(), os.Environ(), "go", "-C", root, "build", "-p", "1", "-ldflags", "-X=main.Version="+featureVersion, "-o", controller, "./cmd/sparkwing-controller")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controller: %v: %s", err, output)
	}
	f := newFeatureController(t, controller, "artifact-cli")
	f.cli = cli
	run := f.plan(runspec.Request{}, []nodeproto.JobSpec{{ID: "producer", Artifacts: []string{"*"}}})
	award := f.mustClaim(f.slow, run, "producer")
	f.start(award)
	host := f.host(award)
	principal, err := host.Principal(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := host.BindStorage(f.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	name := "report\x1b[31m\a\n.txt"
	payload := []byte("team artifact\n")
	for path, data := range map[string][]byte{name: payload, "slow.bin": bytes.Repeat([]byte("s"), 64*1024)} {
		if err := os.WriteFile(filepath.Join(workspace, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := artifacts.Capture(f.ctx, artifacts.Data{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: storage.Authority.Upload.Value, Pipeline: award.Request.Request.Pipeline}, workspace, []string{"*"}, func(message string) { t.Error(message) })
	if err != nil {
		t.Fatal(err)
	}
	f.report(award, &controllerwire.JobResult{Outcome: "success", Manifest: &manifest.Key})
	f.awaitStatus(run, "success")

	profile := func(name, token, endpoint string) {
		t.Helper()
		cmd := procenv.ControlCommandContext(f.ctx, f.env, cli, "profiles", "add", "--profile", name, "--controller", endpoint, "--token-stdin", "-o", "json")
		cmd.Stdin = strings.NewReader(token + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("profile: %v: %s", err, out)
		}
	}
	profile("second", f.second, f.url)
	profile("outsider", f.member, f.url)
	command := func(profile string, args ...string) ([]byte, error) {
		t.Helper()
		flags := append([]string{"runs", "artifacts", "--profile", profile, "--run", run}, args...)
		return procenv.ControlCommandContext(f.ctx, f.env, cli, flags...).CombinedOutput()
	}
	for _, format := range []string{"plain", "pretty", "json"} {
		out, err := command("second", "-o", format)
		if err != nil {
			t.Fatalf("second-member list %s: %v: %s", format, err, out)
		}
		if bytes.ContainsAny(out, "\x1b\a") || format != "json" && !strings.Contains(string(out), strconv.Quote(name)) {
			t.Fatalf("unsafe terminal listing %s: %q", format, out)
		}
		if format == "json" {
			decoder := json.NewDecoder(bytes.NewReader(out))
			found := false
			for decoder.More() {
				var row struct{ Path string }
				if err := decoder.Decode(&row); err != nil {
					t.Fatal(err)
				}
				found = found || row.Path == name
			}
			if !found {
				t.Fatal("JSON lost the original control-character path")
			}
		}
	}
	dest := filepath.Join(t.TempDir(), "download")
	if out, err := command("second", "--job", "producer", "--path", name, "--dest", dest, "-o", "json"); err != nil {
		t.Fatalf("second-member download: %v: %s", err, out)
	}
	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("download bytes: %q: %v", got, err)
	}

	state := ""
	for _, entry := range f.env {
		if value, ok := strings.CutPrefix(entry, "SPARKWING_HOME="); ok {
			state = value
		}
	}
	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	teststore.RegisterSQLite(t, s, filepath.Join(state, "state.db"))
	if _, err := teststore.Exec(f.ctx, s, "DELETE FROM memberships WHERE team=? AND account_id=?", store.DefaultTeam, "actor-one"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-o", "json"}, {"--job", "producer", "--path", name, "--dest", filepath.Join(t.TempDir(), "forbidden")}} {
		if out, err := command("outsider", args...); err == nil {
			t.Fatalf("non-member accessed artifacts: %s", out)
		}
	}
	t.Logf("run=%s: second member listed/downloaded exact bytes; removed member refused; terminal paths escaped", run)

	t.Run("interrupt_stream", func(t *testing.T) {
		streaming := make(chan struct{})
		stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(64*1024))
			_, _ = w.Write([]byte("s"))
			w.(http.Flusher).Flush()
			close(streaming)
			<-r.Context().Done()
		}))
		defer stream.Close()
		target, err := url.Parse(f.url)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			if response.StatusCode != http.StatusOK || !strings.HasSuffix(response.Request.URL.Path, "/artifacts") || response.Request.URL.Query().Get("path") == "" {
				return nil
			}
			var result controllerwire.JobArtifacts
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				return err
			}
			_ = response.Body.Close()
			result.Download.URL = stream.URL
			body, err := json.Marshal(result)
			if err != nil {
				return err
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			response.ContentLength = int64(len(body))
			response.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return nil
		}
		gateway := httptest.NewServer(proxy)
		defer gateway.Close()
		profile("slow", f.second, gateway.URL)
		dir := t.TempDir()
		dest := filepath.Join(dir, "interrupted")
		cmd := procenv.ControlCommandContext(f.ctx, f.env, cli, "runs", "artifacts", "--profile", "slow", "--run", run, "--job", "producer", "--path", "slow.bin", "--dest", dest, "-o", "json")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		select {
		case <-streaming:
		case err := <-done:
			t.Fatalf("download exited before streaming: %v: %s", err, output.String())
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("destination visible before verified download: %v", err)
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("interrupted download succeeded")
			}
		// sleepcheck:external-boundary SIGINT must unwind the real streaming CLI process
		case <-time.After(10 * time.Second):
			t.Fatal("interrupted CLI did not exit")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("interrupt left download files: %v: %v", entries, err)
		}
		t.Log("SIGINT mid-stream removed temporary bytes and left --dest absent")
	})
}
