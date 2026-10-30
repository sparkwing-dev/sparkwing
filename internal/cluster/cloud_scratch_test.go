package cluster

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestCloudHostRestoresTemporaryDirectoryPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	repository, sha := sourcetest.Repository(t)
	remote := "https://1.1.1.1/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repository})
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runs/run/source-credential" {
			t.Errorf("unexpected controller request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"kind":"https","host":"1.1.1.1","username":"source","secret":"fixture"}`))
	}))
	t.Cleanup(controller.Close)
	options, award := bootstrapPlanFile(t)
	options.ControllerURL = controller.URL
	award.Bootstrap = []byte(productionTestBearer("bootstrap"))
	award.Source = source.Spec{RepoKey: "git:1.1.1.1/team/repo", RemoteURL: remote, SHA: sha, PipelineDir: ".sparkwing"}
	runtime := productionRuntime("dev").Bootstrap
	initializer, err := runtime.Source(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializer.Run(t.Context(), award, options.SourceDir); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	docker := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_ping" {
			t.Errorf("unexpected Docker request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("OK"))
	}))
	if err := docker.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	docker.Listener = listener
	docker.Start()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	if _, _, err := runtime.Host(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(options.SourceDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode(); got.Perm() != 0o777 || got&os.ModeSticky == 0 {
		t.Fatalf("Cloud scratch mode=%v, want sticky and writable by package-manager users", got)
	}
}
