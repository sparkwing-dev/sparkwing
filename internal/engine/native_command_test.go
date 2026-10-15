package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const dependencyToolFixture = `package main
import("encoding/base64";"fmt";"net/url";"os";"strings")
func main(){
 args:=os.Args[1:]
 if len(args)>0 && strings.HasSuffix(args[0],"npm-cli.js") {args=args[1:]}
 if len(args)!=5 || args[0]!="install" || args[1]!="--package-lock-only" || args[2]!="--ignore-scripts" || args[3]!="--no-audit" || args[4]!="--no-fund" {os.Exit(10)}
 proxy:=os.Getenv("HTTPS_PROXY");fmt.Fprintln(os.Stdout,proxy)
 parsed,_:=url.Parse(proxy);if parsed!=nil && parsed.User!=nil {password,_:=parsed.User.Password();fmt.Fprintln(os.Stderr,password,base64.StdEncoding.EncodeToString([]byte(password)))}
 os.Exit(12)
}`

func dependencyTool(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(dependencyToolFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "npm"
	if runtime.GOOS == "windows" {
		name = "node.exe"
		if err := os.WriteFile(filepath.Join(dir, "npm.cmd"), []byte("@exit /b 99\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "build", "-p", "2", "-o", filepath.Join(dir, name), source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build dependency tool: %v: %s", err, output)
	}
	return dir
}

func TestNativeDependencyCommandMasksFailure(t *testing.T) {
	t.Setenv("PATH", dependencyTool(t))
	assertNativeDependencyMask(t, "native-dependency-proxy-secret")
}

func TestNativeDependencyCommandHonorsCancellation(t *testing.T) {
	t.Setenv("PATH", dependencyTool(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := (&Resolver{}).nativeCommand(ctx, t.TempDir(), "npm", "install")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled native tool: %v", err)
	}
}

func FuzzNativeDependencyDiagnostics(f *testing.F) {
	tool := dependencyTool(f)
	f.Add([]byte("credential containing !@#$%^&*() spaces"))
	f.Add([]byte{0, 1, 254, 255})
	f.Fuzz(func(t *testing.T, material []byte) {
		if len(material) > 1024 {
			t.Skip()
		}
		t.Setenv("PATH", tool)
		assertNativeDependencyMask(t, "proxy-credential-"+base64.RawURLEncoding.EncodeToString(material))
	})
}

func assertNativeDependencyMask(t *testing.T, password string) {
	t.Helper()
	proxy := "https://" + url.UserPassword("builder", password).String() + "@proxy.example"
	t.Setenv("HTTPS_PROXY", proxy)
	err := (&Resolver{}).nativeCommand(t.Context(), t.TempDir(), "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 12 {
		t.Fatalf("dependency tool did not run through native argv: %v", err)
	}
	for _, secret := range []string{password, proxy, base64.StdEncoding.EncodeToString([]byte(password))} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("dependency tool diagnostic exposed proxy credentials")
		}
	}
}
