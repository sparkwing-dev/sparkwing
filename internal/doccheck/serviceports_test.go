package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFakeServiceMains(t *testing.T, root string, controllerPort string) {
	t.Helper()
	mains := map[string]string{
		"sparkwing-controller": controllerPort,
		"sparkwing-web":        "4343",
		"sparkwing-logs":       "4345",
	}
	for name, port := range mains {
		dir := filepath.Join(root, "cmd", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := "package main\nfunc main() {\n\taddr := fs.String(\"addr\", \"127.0.0.1:" + port + "\", \"bind address\")\n\t_ = addr\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckServicePorts_PassesWhenTableMatchesCode(t *testing.T) {
	root := t.TempDir()
	writeFakeServiceMains(t, root, "4344")
	content := t.TempDir()
	writeDoc(t, content, "architecture.md",
		"| Controller | `sparkwing-controller.sparkwing.svc.cluster.local` | 80 -> 4344 |\n"+
			"| Logs | `sparkwing-logs.sparkwing.svc.cluster.local` | 80 -> 4345 |\n"+
			"| Dashboard | `sparkwing-web.sparkwing.svc.cluster.local` | 80 -> 4343 |\n")
	if !checkServicePorts(content, root) {
		t.Fatal("expected pass: every documented target port matches the binary --addr default")
	}
}

func TestCheckServicePorts_FailsOnStalePort(t *testing.T) {
	root := t.TempDir()
	writeFakeServiceMains(t, root, "4344")
	content := t.TempDir()
	writeDoc(t, content, "architecture.md",
		"| Controller | `sparkwing-controller.sparkwing.svc.cluster.local` | 80 -> 8080 |\n")
	if checkServicePorts(content, root) {
		t.Fatal("expected failure: controller documented at 8080 but --addr default is 4344")
	}
}

func TestCheckServicePorts_IgnoresLinesWithoutTargetPort(t *testing.T) {
	root := t.TempDir()
	writeFakeServiceMains(t, root, "4344")
	content := t.TempDir()
	writeDoc(t, content, "architecture.md",
		"The `sparkwing-controller` service fronts the controller pod on port 80.\n")
	if !checkServicePorts(content, root) {
		t.Fatal("a service mention without an arrow target port must not be flagged")
	}
}
