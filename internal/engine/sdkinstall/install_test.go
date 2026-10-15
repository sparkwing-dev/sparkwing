package sdkinstall

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

const nativeInstallFixtureSource = `package main
import("fmt";"os";"path/filepath";"strings")
var Version="dev"
func main(){
 args:=os.Args[1:]
 if strings.Contains(filepath.Base(os.Args[0]),"node") && len(args)>0 && strings.HasSuffix(args[0],"npm-cli.js") { args=args[1:] }
 if strings.Contains(filepath.Base(os.Args[0]),"npm") || strings.Contains(filepath.Base(os.Args[0]),"node") {
  if len(args)<1 || args[0]!="ci" { os.Exit(10) }
  dir:=filepath.Join("node_modules","@sparkwing","sdk")
  if os.MkdirAll(dir,0700)!=nil || os.WriteFile(filepath.Join(dir,"package.json"),[]byte("{\"version\":\"0.20.0\"}"),0600)!=nil { os.Exit(11) }
  return
 }
 if len(os.Args)>1 && os.Args[1]=="list" { fmt.Print("{\"Path\":\"github.com/sparkwing-dev/sparkwing\",\"Version\":\"v0.20.0\"}");return }
 for i,arg:=range os.Args { if arg=="-o" && i+1<len(os.Args) { if os.WriteFile(os.Args[i+1],[]byte("locally-built-fixture"),0700)!=nil { os.Exit(12) };fmt.Fprintln(os.Stderr,os.Getenv("HTTPS_PROXY"));return } }
 os.Exit(13)
}`

func TestRunSDKInstallBuildsLocallyAndUsesNativeLockfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("SPARKWING_HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	source := filepath.Join(root, "tool.go")
	if err := os.WriteFile(source, []byte(nativeInstallFixtureSource), 0o600); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(root, "tool.exe")
	build := exec.Command("go", "build", "-p", "2", "-o", toolPath, source)
	if body, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native tool fixture: %s %v", body, err)
	}
	tool, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, name := range []string{"go", "npm", "node"} {
		if runtime.GOOS == "windows" && name == "npm" {
			writeInput(t, bin, "npm.cmd", []byte("@exit /b 99\r\n"))
			continue
		}
		if err := os.WriteFile(filepath.Join(bin, name+suffix), tool, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	secret := "proxy-secret-native-installer"
	t.Setenv("HTTPS_PROXY", "http://user:"+secret+"@proxy.example")
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			dir := t.TempDir()
			if sdk == engine.GoSDK {
				writeInput(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire "+engine.GoModule+" v0.20.0\n"))
			} else {
				writeInput(t, dir, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"0.20.0"}},"node_modules/@sparkwing/sdk":{"version":"0.20.0"}}}`))
			}
			if sdk == engine.TypeScriptSDK {
				writeInput(t, dir, "package.json", []byte(`{"main":"main.ts","dependencies":{"@sparkwing/sdk":"0.20.0"}}`))
				writeInput(t, dir, "main.ts", []byte("export {}"))
			} else {
				writeInput(t, dir, "main.go", []byte("package main\nfunc main(){}"))
			}
			body, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: sdk})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if err := Run(t.Context(), "v0.20.0", bytes.NewReader(body), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			var command engine.SDKCommand
			if err := json.Unmarshal(stdout.Bytes(), &command); err != nil {
				t.Fatal(err)
			}
			if command.Directory != dir || len(command.Command) == 0 || !filepath.IsAbs(command.Command[0]) || strings.Contains(stderr.String(), secret) {
				t.Fatalf("command=%+v stderr=%s", command, stderr.String())
			}
			if sdk == engine.TypeScriptSDK {
				if len(command.Command) != 2 || command.Command[1] != filepath.Join(dir, "main.ts") {
					t.Fatalf("TypeScript retained a preload: %+v", command)
				}
			} else {
				first := command.Command[0]
				if err := os.Remove(filepath.Join(bin, "go"+suffix)); err != nil {
					t.Fatal(err)
				}
				stdout.Reset()
				if err := Run(t.Context(), "v0.20.0", bytes.NewReader(body), &stdout, &stderr); err != nil {
					t.Fatalf("local binary cache miss: %v", err)
				}
				if err := json.Unmarshal(stdout.Bytes(), &command); err != nil || first != command.Command[0] {
					t.Fatalf("cached command changed: %+v, %v", command, err)
				}
			}
			for _, input := range [][]byte{[]byte(`{"directory":"a","directory":"b"}`), []byte(`{"directory":"a","Directory":"b","sdk":"go"}`), []byte(`{"sdk":"go","SDK":"typescript"}`), []byte(`{"environment":["TOKEN=credential"]}`), append(bytes.Clone(body), []byte(" {}")...)} {
				stdout.Reset()
				if err := Run(t.Context(), "v0.20.0", bytes.NewReader(input), &stdout, &stderr); err == nil || stdout.Len() != 0 {
					t.Fatalf("invalid installation returned a command: %v", err)
				}
			}
			stdout.Reset()
			if err := Run(t.Context(), "v0.10.0", bytes.NewReader(body), &stdout, &stderr); err == nil || stdout.Len() != 0 {
				t.Fatalf("wrong engine version returned a command: %v", err)
			}
		})
	}
}

func writeInput(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}
