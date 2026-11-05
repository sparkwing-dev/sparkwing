package sparkwing_test

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// `go mod tidy` in a pipeline module loads every package the SDK imports and
// those packages' tests, then downloads each module they reach. One test that
// imported the controller once made every new pipeline download 115 MB of
// modules (sqlite, AWS, Kubernetes, gRPC) it never compiles.
func TestSDKTidyClosureNeedsOnlySmallModules(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	run := func(args ...string) []string {
		t.Helper()
		cmd := exec.Command(gobin, args...)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go %s: %v", strings.Join(args, " "), err)
		}
		return strings.Fields(string(out))
	}
	const module = "github.com/sparkwing-dev/sparkwing"
	var closure []string
	for _, pkg := range run("list", "-deps", module+"/sparkwing") {
		if strings.HasPrefix(pkg, module+"/") {
			closure = append(closure, pkg)
		}
	}
	allowed := []string{module, "golang.org/x/crypto", "golang.org/x/sys", "golang.org/x/mod", "go.uber.org/goleak", "go.yaml.in/yaml/v3"}
	modules := run(append([]string{"list", "-deps", "-test", "-f", "{{with .Module}}{{.Path}}{{end}}"}, closure...)...)
	var extra []string
	for _, m := range modules {
		if !slices.Contains(allowed, m) && !slices.Contains(extra, m) {
			extra = append(extra, m)
		}
	}
	if len(extra) > 0 {
		t.Fatalf("a package in the SDK's import closure, or its tests, reaches modules a pipeline never needs: %v.\n"+
			"Move the test that imports them to a package outside the closure (go mod why -m <module> from a pipeline names it).", extra)
	}
}

// Every pipeline compiles the SDK's standard-library imports on a cold build
// cache. net/http brings crypto/tls, crypto/x509 and mime, roughly 45% of an
// uncached first compile on a two-vCPU CI runner, though the SDK only talks
// to its node host over loopback. sparkwingtest compiles only into a
// pipeline's tests, so it is left out.
func TestSDKCompileClosureSkipsHeavyStandardPackages(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	cmd := exec.Command(gobin, "list", "-deps", ".", "./cleanup", "./docker", "./git", "./inputs", "./services")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		switch pkg {
		case "net/http", "crypto/tls", "crypto/x509", "mime/multipart":
			t.Errorf("the SDK's import closure reaches %s; keep server and HTTP client code outside it "+
				"(go list -deps -f '{{.ImportPath}}: {{.Imports}}' ./sparkwing names the importer)", pkg)
		}
	}
}
