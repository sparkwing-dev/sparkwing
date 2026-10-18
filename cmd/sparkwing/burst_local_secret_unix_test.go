//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
