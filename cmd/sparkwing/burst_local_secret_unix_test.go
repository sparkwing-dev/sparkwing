//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

const signingMaterial = "signing-material-4f1c9e"

func TestBurstPreparationWithholdsConfiguredLocalSecretFromInstallAndPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SIGNING_MATERIAL", signingMaterial)
	marker := filepath.Join(t.TempDir(), "install-saw-secret")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	wrapper := "#!/bin/sh\nif [ -n \"${SIGNING_MATERIAL+set}\" ]; then : > " + marker + "; fi\nexec " + realGo + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(tools, "go"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := burstIntakeRepository(t)
	pipeline := strings.ReplaceAll(burstIntakePipeline, "TOKEN", "SIGNING_MATERIAL")
	pipeline = strings.Replace(pipeline, "func main() {\n", "func main() {\n    if _, leaked := os.LookupEnv(\"SIGNING_MATERIAL\"); leaked { os.Exit(97) }\n", 1)
	writeSnapshotFile(t, repo, ".sparkwing/main.go", pipeline, 0o600)
	probe := filepath.Join(t.TempDir(), "fsmonitor-output")
	monitor := filepath.Join(t.TempDir(), "fsmonitor")
	callback := "#!/bin/sh\nprintf 'seen:%s\\n' \"${SIGNING_MATERIAL-}\" >> '" + probe + "'\nprintf 'token\\0/\\0'\n"
	if err := os.WriteFile(monitor, []byte(callback), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte("[core]\n fsmonitor = "+strconv.Quote(monitor)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	_, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	var sent bytes.Buffer
	transport := logRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			sent.Write(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	options := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: transport}}
	route := userconfig.RunRouting{Profile: new("cloud"), LocalSecrets: []string{"SIGNING_MATERIAL"}, SourceFiles: []string{"declared.txt"}}
	prepared, err := prepareNativeBurst(t.Context(), "burst", runFlags{}, nil, route, options)
	observed, readErr := os.ReadFile(probe)
	if readErr != nil || !bytes.Contains(observed, []byte("seen:")) {
		t.Fatalf("Git did not execute the global fsmonitor callback: %v", readErr)
	}
	if bytes.Contains(observed, []byte(signingMaterial)) {
		t.Fatal("configured local secret reached Git's global fsmonitor callback")
	}
	if err != nil {
		t.Fatalf("burst preparation exposed or lost the configured local secret: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configured local secret reached SDK installation")
	}
	if prepared.LocalSecrets["SIGNING_MATERIAL"] != signingMaterial || !slices.Equal(prepared.Request.LocalSecretNames, []string{"SIGNING_MATERIAL"}) {
		t.Fatalf("authorized secret release lost the configured secret: %v", prepared.Request.LocalSecretNames)
	}
	request, err := json.Marshal(prepared.Request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request, []byte(signingMaterial)) || bytes.Contains(sent.Bytes(), []byte(signingMaterial)) {
		t.Fatal("configured local secret entered the submitted request")
	}
}

func FuzzWithholdLocalSecretsLeavesNoConfiguredName(f *testing.F) {
	f.Add("SIGNING_MATERIAL", "value", "OTHER", "ordinary")
	f.Add("A", "", "A", "duplicate")
	f.Fuzz(func(t *testing.T, name, value, other, ordinary string) {
		if name == "" || name == other || strings.ContainsAny(name+other, "=\x00") || strings.ContainsRune(value+ordinary, 0) {
			t.Skip()
		}
		masker := secrets.NewMasker()
		environment, held := withholdLocalSecrets([]string{name + "=" + value, other + "=" + ordinary, "MALFORMED"}, []string{name}, masker)
		for _, item := range environment {
			if key, _, _ := strings.Cut(item, "="); key == name {
				t.Fatal("configured local secret remained in the environment")
			}
		}
		if held[name] != value {
			t.Fatal("configured local secret was not held for release")
		}
		if other != name && !slices.Contains(environment, other+"="+ordinary) {
			t.Fatal("ordinary environment was dropped")
		}
	})
}

func TestLocalEntryPointsWithholdSigningMaterial(t *testing.T) {
	for _, entry := range []string{"help", "describe", "plan", "preparation", "trigger", "trigger-working-tree"} {
		t.Run(entry, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("SPARKWING_HOME", home)
			config := filepath.Join(home, "config.yaml")
			t.Setenv("SPARKWING_CONFIG", config)
			if err := os.WriteFile(config, []byte("profiles:\n  cloud:\n    controller:\n      url: https://controller.example\n      token: private-account-credential\nrun:\n  routing:\n    local_secrets: [SIGNING_MATERIAL]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SIGNING_MATERIAL", signingMaterial)
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			tools := t.TempDir()
			marker := filepath.Join(tools, "leaked")
			wrapper := "#!/bin/sh\nif [ -n \"${SIGNING_MATERIAL+set}\" ]; then : > " + marker + "; fi\nif [ \"$1\" = mod ] && [ \"$2\" = download ] && [ \"$4\" = all ]; then exit 0; fi\nexec " + realGo + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(tools, "go"), []byte(wrapper), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			repo := burstIntakeRepository(t)
			configBody, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "sparkwing.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			configBody = append(configBody, []byte("sparks:\n  - name: sdk\n    source: github.com/sparkwing-dev/sparkwing\n    version: v0.0.0\n")...)
			writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", string(configBody), 0o600)
			sums, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.sum"))
			if err != nil {
				t.Fatal(err)
			}
			writeSnapshotFile(t, repo, ".sparkwing/.resolved.sum", string(sums), 0o600)
			pipeline := strings.ReplaceAll(burstIntakePipeline, "TOKEN", "SIGNING_MATERIAL")
			pipeline = strings.Replace(pipeline, "func main() {\n", "func main() {\n if _, leaked := os.LookupEnv(\"SIGNING_MATERIAL\"); leaked { os.Exit(97) }\n", 1)
			writeSnapshotFile(t, repo, ".sparkwing/main.go", pipeline, 0o600)
			t.Chdir(repo)
			args := []string{"run", "burst", "--help"}
			if entry == "describe" || entry == "plan" {
				args = []string{"pipeline", entry, "--pipeline", "burst"}
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			switch entry {
			case "help":
				err = dispatchRunWithEngine(inv, func(string, func()) error { t.Fatal("help selected execution engine"); return nil })
			case "describe":
				err = runPipelineDescribe(inv)
			case "plan":
				err = runPipelinePlan(inv)
			case "trigger", "trigger-working-tree":
				runSnapshotGit(t, repo, "add", ".sparkwing")
				runSnapshotGit(t, repo, "commit", "--quiet", "-m", "trigger pipeline")
				runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
				if entry == "trigger-working-tree" {
					if err := os.Remove(filepath.Join(repo, ".env")); err != nil {
						t.Fatal(err)
					}
					writeSnapshotFile(t, repo, "ordinary.txt", "public", 0o600)
					runSnapshotGit(t, repo, "add", "ordinary.txt")
					runSnapshotGit(t, repo, "commit", "--quiet", "-m", "ordinary source")
					writeSnapshotFile(t, repo, "ordinary.txt", signingMaterial, 0o600)
					previous := http.DefaultTransport
					requests := 0
					http.DefaultTransport = logRoundTrip(func(*http.Request) (*http.Response, error) {
						requests++
						return nil, fmt.Errorf("unexpected source publication")
					})
					defer func() { http.DefaultTransport = previous }()
					_, err = prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "burst", profile: "cloud", workingTree: true})
					if err == nil || !strings.Contains(err.Error(), "source bundle contains a declared local secret") || requests != 0 {
						t.Fatalf("working-tree trigger bypassed known-secret boundary: requests=%d err=%v", requests, err)
					}
					return
				}
				_, err = prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "burst"})
			case "preparation":
				var snapshots *nodehost.FileEnvironmentSnapshots
				snapshots, err = nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
				if err == nil {
					_, err = prepareNativeLocalRun(t.Context(), home, filepath.Join(repo, ".sparkwing"), "burst", os.Environ(), snapshots, runFlags{})
				}
			}
			if err != nil {
				t.Fatalf("%s exposed configured local secret: %v", entry, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("configured local secret reached SDK installation")
			}
		})
	}
}

func FuzzPreparationGitNeverRestoresSigningMaterial(f *testing.F) {
	f.Add([]byte("opaque-value"))
	f.Add([]byte{0, 255, '\n', '\''})
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) > 128 {
			t.Skip()
		}
		secret := "signing-" + fmt.Sprintf("%x", value)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		repo, _ := triggerPreparationRepo(t)
		probe := filepath.Join(t.TempDir(), "fsmonitor-output")
		monitor := filepath.Join(t.TempDir(), "fsmonitor")
		callback := "#!/bin/sh\nprintf 'seen:%s\\n' \"${SIGNING_MATERIAL-}\" >> '" + probe + "'\nprintf 'token\\0/\\0'\n"
		if err := os.WriteFile(monitor, []byte(callback), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte("[core]\n fsmonitor = "+strconv.Quote(monitor)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SIGNING_MATERIAL", secret)
		prepared := prepareLocalEnvironment(os.Environ(), []string{"SIGNING_MATERIAL"})
		t.Setenv("SIGNING_MATERIAL", secret+"-ambient")
		t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "foreign-index"))
		snapshot, err := burstSourceSnapshot(t.Context(), prepared.captured, repo, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := snapshot.close(); err != nil {
				t.Error(err)
			}
		}()
		if err := checkSnapshotSecrets(snapshot, t.Context(), map[string]string{"SIGNING_MATERIAL": secret}); err != nil {
			t.Fatal(err)
		}
		preparedTriggerCheckout(t, snapshot.environment, snapshot.RepoRoot, snapshot.SHA, snapshot.BundlePath)
		observed, err := os.ReadFile(probe)
		if err != nil || !bytes.Contains(observed, []byte("seen:")) {
			t.Fatalf("fsmonitor never ran: %v", err)
		}
		if bytes.Contains(observed, []byte(secret)) {
			t.Fatal("preparation restored a withheld secret")
		}
	})
}
