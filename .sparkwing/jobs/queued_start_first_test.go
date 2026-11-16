package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/modfile"
)

func TestQueuedLandStartsHintedPackagesBeforeOtherBodies(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "go.mod"), "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/go.mod"), "module sparkwing-pipelines\n\ngo 1.26.0\n")
	var mu sync.Mutex
	var started []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		started = append(started, strings.TrimPrefix(r.URL.Path, "/"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	for _, fixture := range []struct{ dir, name string }{
		{"a_first", "root-other-first"},
		{"tests/acceptance", "acceptance"},
		{"internal/bincache", "bincache"},
		{"internal/store", "store"},
		{"internal/controller", "controller"},
		{"cmd/sparkwing", "cli"},
		{"z_last", "root-other-last"},
		{".sparkwing", "pipeline-other"},
		{".sparkwing/jobs", "pipeline-jobs"},
	} {
		pkg := "fixture"
		if fixture.dir == ".sparkwing" {
			pkg = "pipelines"
		}
		writeGoFile(t, filepath.Join(root, fixture.dir, "fixture_test.go"), fmt.Sprintf(`package %s
import ("net/http"; "testing")
func TestBody(t *testing.T) {
 response,err:=http.Get(%q);if err!=nil { t.Fatal(err) };response.Body.Close()
 if response.StatusCode!=http.StatusOK { t.Fatal(response.Status) }
}

`, pkg, server.URL+"/"+fixture.name))
	}
	gitCommitAll(t, root, "queued admission-order fixture")
	const prefix = "github.com/sparkwing-dev/sparkwing/"
	plans := []affectedTestPlan{
		{module: ".", packages: []string{prefix + "a_first", prefix + "tests/acceptance", prefix + "internal/bincache", storeTestPackage, prefix + "internal/controller", prefix + "cmd/sparkwing", prefix + "z_last"}},
		{module: ".sparkwing", packages: []string{"sparkwing-pipelines", "sparkwing-pipelines/jobs"}},
	}
	ctx, cancel := context.WithTimeout(newTestContext(t), 2*time.Minute)
	defer cancel()
	err := withStableProductTestHome("queued-start-first", func(home string) error {
		var timings goTestTimings
		_, err := runQueuedTestPackages(ctx, plans, hostShape{cpus: 4}, home, &timings)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var rootBodies, pipelineBodies []string
	for _, name := range started {
		if strings.HasPrefix(name, "pipeline-") {
			pipelineBodies = append(pipelineBodies, name)
		} else {
			rootBodies = append(rootBodies, name)
		}
	}
	if want := []string{"cli", "controller", "store", "bincache", "acceptance", "root-other-first", "root-other-last"}; !slices.Equal(rootBodies, want) {
		t.Errorf("hinted root bodies started in order %v, want %v", rootBodies, want)
	}
	if want := []string{"pipeline-jobs", "pipeline-other"}; !slices.Equal(pipelineBodies, want) {
		t.Errorf("hinted pipeline bodies started in order %v, want %v", pipelineBodies, want)
	}
}

func TestQueuedStartFirstHintsNameExistingPackages(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	directories, err := committedModuleDirs(newTestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	modules := map[string]string{}
	for _, directory := range directories {
		body, err := os.ReadFile(filepath.Join(root, directory, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		modules[modfile.ModulePath(body)] = directory
	}
	for _, hint := range queuedStartFirstPackages {
		var owner string
		for module := range modules {
			if module != "" && (hint == module || strings.HasPrefix(hint, module+"/")) && len(module) > len(owner) {
				owner = module
			}
		}
		if owner == "" {
			t.Errorf("start-first hinted package %q has no committed Go module", hint)
			continue
		}
		relative := filepath.Join(modules[owner], strings.TrimPrefix(strings.TrimPrefix(hint, owner), "/"))
		if owningModule(filepath.ToSlash(relative), directories) != modules[owner] {
			t.Errorf("start-first hinted package %q moved into another module", hint)
			continue
		}
		directory := filepath.Join(root, relative)
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Errorf("start-first hinted package %q is missing: %v", hint, err)
			continue
		}
		if !slices.ContainsFunc(entries, func(entry os.DirEntry) bool { return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") }) {
			t.Errorf("start-first hinted package %q has no Go source", hint)
		}
	}
}
