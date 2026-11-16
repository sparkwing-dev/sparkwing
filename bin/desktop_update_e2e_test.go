//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDesktopUpdaterRealIntakeRaceAndRollback(t *testing.T) {
	testmanager.Require(t)
	cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY")
	if cli == "" {
		t.Skip("set SPARKWING_LOCAL_E2E_BINARY to a CLI built from this worktree")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback_%t", rollback), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			dir := t.TempDir()
			home, state, tools, dest, repo := filepath.Join(dir, "home"), filepath.Join(dir, "state"), filepath.Join(dir, "tools"), filepath.Join(dir, "install"), filepath.Join(dir, "repo")
			for _, path := range []string{home, state, tools, dest, filepath.Join(repo, ".sparkwing")} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(home, "config.yaml"), "admission: {budget: ignore-external}\ndashboard: {address: '127.0.0.1:0'}\n")
			if err := os.Chmod(filepath.Join(home, "config.yaml"), 0o600); err != nil {
				t.Fatal(err)
			}
			environment := append(os.Environ(), "HOME="+home, "SPARKWING_HOME="+state, "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"), "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(home, "key"), "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "GOWORK=off", "GOFLAGS=-buildvcs=false -p=1", "GOMAXPROCS=2", "UPDATER_FIXTURE="+dir, "UPDATER_CLI="+cli)
			await := func(label string, condition func() bool) {
				t.Helper()
				for !condition() {
					select {
					case <-ctx.Done():
						t.Fatalf("await %s: %v", label, ctx.Err())
					// sleepcheck:external-boundary coordinate updater and CLI subprocess markers
					case <-time.After(25 * time.Millisecond):
					}
				}
			}
			exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
			var stopController func()
			startController := func() {
				t.Helper()
				log, err := os.Create(filepath.Join(dir, "controller.log"))
				if err != nil {
					t.Fatal(err)
				}
				cmd := procenv.ControlCommandContext(ctx, environment, cli, "wingd", "run", "--home", state)
				cmd.Stdout, cmd.Stderr = log, log
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				stopController = func() {
					_ = cmd.Process.Signal(os.Interrupt)
					select {
					case <-done:
					// sleepcheck:external-boundary join the private controller before replacing it
					case <-time.After(15 * time.Second):
						_ = cmd.Process.Kill()
						<-done
						t.Error("private controller required forced cleanup")
					}
					_ = log.Close()
				}
				await("private controller ready", func() bool {
					status, err := wingdclient.QueryLocalController(ctx, state)
					return err == nil && status.Running
				})
			}
			t.Cleanup(func() {
				write(filepath.Join(dir, "allow-swap"), "")
				write(filepath.Join(dir, "restarted"), "")
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				if !rollback {
					if out, err := procenv.ControlCommandContext(cleanup, environment, cli, "daemon", "stop", "-o", "json").CombinedOutput(); err != nil {
						t.Errorf("private daemon cleanup: %v: %s", err, out)
					}
				}
				if stopController != nil {
					stopController()
				}
			})
			installed := `#!/bin/bash
set -eu
if [ "$1 $2" = 'daemon restart' ]; then
 touch "$UPDATER_FIXTURE/restart-requested"
 while [ ! -e "$UPDATER_FIXTURE/restarted" ]; do sleep 0.02; done
 exit
fi
exec "$UPDATER_CLI" "$@"
`
			if rollback {
				installed = "#!/bin/sh\necho '{\"kind\":\"service\",\"tool\":\"sparkwing\",\"service\":\"controller\",\"state\":\"stopped\",\"activity\":{\"runs\":0,\"queued_triggers\":0,\"held_leases\":0}}'\n"
			}
			write(filepath.Join(dest, "sparkwing"), installed)
			write(filepath.Join(dest, "sparkwing-runner"), "old runner\n")
			write(filepath.Join(dir, "candidate"), installed+"\n# candidate\n")
			write(filepath.Join(dir, "installer"), "#!/bin/bash\nset -eu\nmkdir -p \"$SPARKWING_INSTALL_BIN\"\ncp \"$UPDATER_FIXTURE/candidate\" \"$SPARKWING_INSTALL_BIN/sparkwing\"\nprintf 'new runner\\n' > \"$SPARKWING_INSTALL_BIN/sparkwing-runner\"\nchmod +x \"$SPARKWING_INSTALL_BIN/sparkwing-runner\"\n")
			write(filepath.Join(tools, "git"), `#!/bin/bash
set -eu
case "$3" in
 remote) echo fixture-origin;;
 rev-parse) echo fixture-main;;
 clone) target="${@: -1}"; mkdir -p "$target/bin"; cp "$UPDATER_FIXTURE/installer" "$target/bin/install.sh";;
 *) exit 90;;
esac
`)
			write(filepath.Join(tools, "gh"), "#!/bin/sh\necho '[{\"headSha\":\"fixture-main\",\"status\":\"completed\",\"conclusion\":\"success\"}]'\n")
			write(filepath.Join(tools, "systemctl"), "#!/bin/sh\nset -eu\n[ \"$2\" = list-units ]\necho '[]'\ntouch \"$UPDATER_FIXTURE/idle-snapshot\"\n")
			if rollback {
				write(filepath.Join(tools, "mv"), "#!/bin/bash\nif [[ \"${@: -1}\" == \"$UPDATER_FIXTURE/install/sparkwing\" ]]; then exit 30; fi\nexec /bin/mv \"$@\"\n")
				write(filepath.Join(dir, "allow-swap"), "")
			} else {
				write(filepath.Join(tools, "mv"), "#!/bin/bash\nif [[ \"${@: -1}\" == \"$UPDATER_FIXTURE/install/sparkwing-runner\" ]]; then\n while [ ! -e \"$UPDATER_FIXTURE/allow-swap\" ]; do sleep 0.02; done\nfi\nexec /bin/mv \"$@\"\n")
				write(filepath.Join(repo, ".sparkwing/go.mod"), fmt.Sprintf("module updater-land\ngo 1.26.0\nrequire github.com/sparkwing-dev/sparkwing v0.72.0\nreplace github.com/sparkwing-dev/sparkwing => %s\n", root))
				write(filepath.Join(repo, ".sparkwing/main.go"), `package main
import("context";"os"; sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type Land struct{sw.Base}
func(*Land)Plan(_ context.Context,p *sw.Plan,_ struct{},_ sw.RunContext)error{sw.Job(p,"finish",&Finish{});return nil}
type Finish struct{sw.Base}
func(*Finish)Work(w *sw.Work)(*sw.WorkStep,error){sw.Step(w,"finish",func(context.Context)error{return os.WriteFile(os.Getenv("UPDATER_FIXTURE")+"/land-finished",[]byte("survived"),0600)});return nil,nil}
func main(){sw.Register[struct{}]("Land",func()sw.Pipeline[struct{}]{return &Land{}});sw.Main()}
`)
				write(filepath.Join(repo, ".sparkwing/sparkwing.yaml"), "pipelines:\n - {name: land, entrypoint: Land}\n")
				for _, args := range [][]string{{"go", "-C", filepath.Join(repo, ".sparkwing"), "mod", "tidy"}, {"git", "-C", repo, "init", "-q", "-b", "main"}, {"git", "-C", repo, "add", "."}, {"git", "-C", repo, "-c", "user.name=Acceptance", "-c", "user.email=acceptance@example.invalid", "-c", "commit.gpgsign=false", "commit", "--no-verify", "-qm", "fixture"}} {
					if out, err := procenv.ControlCommandContext(ctx, environment, args[0], args[1:]...).CombinedOutput(); err != nil {
						t.Fatalf("fixture setup: %v: %s", err, out)
					}
				}
				startController()
			}
			var proxyServer *http.Server
			var listener net.Listener
			intake := make(chan struct{}, 1)
			if !rollback {
				socket, err := wingd.APISocketPath(state)
				if err != nil {
					t.Fatal(err)
				}
				upstream := socket + ".upstream"
				if err := os.Rename(socket, upstream); err != nil {
					t.Fatal(err)
				}
				listener, err = net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(socket, 0o600); err != nil {
					t.Fatal(err)
				}
				proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "sparkwing-api"})
				transport := wingdclient.NewAPIClient(upstream).Transport.(*http.Transport)
				defer transport.CloseIdleConnections()
				proxy.Transport = transport
				proxyServer = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs" && exists("idle-snapshot") {
						select {
						case intake <- struct{}{}:
						default:
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"controller stopping"}}`))
						return
					}
					proxy.ServeHTTP(w, r)
				})}
				go func() { _ = proxyServer.Serve(listener) }()
				defer proxyServer.Close()
			}
			workerEnv := append(environment, "PATH="+tools+":"+os.Getenv("PATH"))
			worker := procenv.ControlCommandContext(ctx, workerEnv, "bash", filepath.Join(root, "bin/desktop-update.sh"), repo, dest, "60", state)
			var workerOutput bytes.Buffer
			worker.Stdout, worker.Stderr = &workerOutput, &workerOutput
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			workerDone := make(chan error, 1)
			go func() { workerDone <- worker.Wait() }()
			t.Cleanup(func() { _ = worker.Process.Kill() })
			if rollback {
				err := <-workerDone
				oldCLI, _ := os.ReadFile(filepath.Join(dest, "sparkwing"))
				oldRunner, _ := os.ReadFile(filepath.Join(dest, "sparkwing-runner"))
				if err == nil || string(oldCLI) != installed || string(oldRunner) != "old runner\n" || exists("restart-requested") || !strings.Contains(workerOutput.String(), "previous binaries restored") {
					t.Fatalf("rollback failed: %v: %s", err, workerOutput.String())
				}
				t.Log("second rename failed: both old binaries restored; no restart")
				return
			}
			await("idle snapshot", func() bool {
				select {
				case err := <-workerDone:
					t.Fatalf("worker exited before idle: %v: %s", err, workerOutput.String())
				default:
				}
				return exists("idle-snapshot")
			})
			land := procenv.ControlCommandContext(ctx, environment, cli, "run", "land", "-C", repo, "--sw-detached", "--sw-no-update", "-o", "json")
			var landOutput, landError bytes.Buffer
			land.Stdout, land.Stderr = &landOutput, &landError
			if err := land.Start(); err != nil {
				t.Fatal(err)
			}
			landDone := make(chan error, 1)
			go func() { landDone <- land.Wait() }()
			t.Cleanup(func() { _ = land.Process.Kill() })
			select {
			case <-intake:
			case err := <-landDone:
				t.Fatalf("land exited before restart race: %v: %s %s", err, landOutput.String(), landError.String())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			started := time.Now()
			write(filepath.Join(dir, "allow-swap"), "")
			await("restart requested", func() bool {
				select {
				case err := <-workerDone:
					t.Fatalf("worker exited before restart: %v: %s", err, workerOutput.String())
				default:
				}
				return exists("restart-requested")
			})
			stopController()
			stopController = nil
			_ = proxyServer.Close()
			select {
			// sleepcheck:external-boundary a real controller outage must outlast the old 700ms intake budget
			case <-time.After(20 * time.Second):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			startController()
			write(filepath.Join(dir, "restarted"), "")
			if err := <-workerDone; err != nil {
				t.Fatalf("updater failed: %v: %s", err, workerOutput.String())
			}
			if err := <-landDone; err != nil {
				t.Fatalf("land intake lost after %s: %v: %s %s", time.Since(started), err, landOutput.String(), landError.String())
			}
			var receipt struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(landOutput.Bytes(), &receipt); err != nil || receipt.RunID == "" {
				t.Fatalf("land receipt: %v: %s", err, landOutput.String())
			}
			await("land execution", func() bool { return exists("land-finished") })
			await("land success", func() bool {
				out, err := procenv.ControlCommandContext(ctx, environment, cli, "runs", "status", "--run", receipt.RunID, "-o", "json").Output()
				if err != nil {
					return false
				}
				var status struct {
					Status string `json:"status"`
				}
				return json.Unmarshal(out, &status) == nil && status.Status == "success"
			})
			t.Logf("land=%s admitted after %s and executed after updater restart", receipt.RunID, time.Since(started))
		})
	}
}
