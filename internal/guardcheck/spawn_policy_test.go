package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestPipelineProcessesRequirePolicyConstructors(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module " + modulePath + "\n\ngo 1.26.0\n",
		"internal/procenv/env.go": `package procenv
import ("context"; "os/exec")
type Env struct { values []string }
func Build() (Env,error) { return Env{},nil }
func Command(env Env, name string) *exec.Cmd { return nil }
func CommandContext(ctx context.Context, env Env, name string) *exec.Cmd { return nil }
func HostCommand(name string) *exec.Cmd { return nil }
func HostCommandContext(ctx context.Context, name string) *exec.Cmd { return nil }
func ControlCommand(env []string,name string) *exec.Cmd { return nil }
func ControlCommandContext(ctx context.Context,env []string,name string) *exec.Cmd { return nil }
`,
		"internal/engine/env.go": `package engine
import ("context"; "os"; p "github.com/sparkwing-dev/sparkwing/internal/procenv")
func good(ctx context.Context) { env,_ := p.Build(); factory := p.Command; factory(env,"go"); p.CommandContext(ctx,env,"go") }
func host(ctx context.Context) { p.HostCommand("go"); p.HostCommandContext(ctx,"go") }
func control(ctx context.Context) { p.ControlCommand(os.Environ(),"go"); p.ControlCommandContext(ctx,os.Environ(),"go") }
var factory = p.HostCommand
`,
	}
	for _, name := range []string{"repos", "pipelineentry", "nodehost", "engine/subpackage"} {
		files["internal/"+name+"/env.go"] = files["internal/engine/env.go"]
	}
	for name, source := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgs, err := packages.Load(&packages.Config{Dir: root, Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}, "./internal/...")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 6 {
		t.Fatalf("load: %+v", pkgs)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) != 0 {
			t.Fatalf("load %s: %v", pkg.PkgPath, pkg.Errors)
		}
		if !pipelinePackage(pkg.PkgPath) {
			continue
		}
		findings := packageSpawnFindings(pkg)
		if len(findings) != 5 {
			t.Fatalf("%s: want 5 policy bypasses, got %d:\n%s", pkg.PkgPath, len(findings), strings.Join(findings, "\n"))
		}
		for _, want := range []string{"HostCommand", "HostCommandContext", "ControlCommand", "ControlCommandContext"} {
			if !strings.Contains(strings.Join(findings, "\n"), want) {
				t.Errorf("missing %q in %v", want, findings)
			}
		}
	}
}
