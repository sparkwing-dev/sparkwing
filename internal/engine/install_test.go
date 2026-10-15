package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

const installerFixtureSource = `package main
import("encoding/json";"fmt";"os";"path/filepath")
var Version="dev"
func main(){
 if len(os.Args)!=2 || os.Args[1]!="__sdk-install" { os.Exit(20) }
 var req struct{Directory string;PipelineDir string ` + "`json:\"pipeline_dir\"`" + `;SDK string}
 dec:=json.NewDecoder(os.Stdin); dec.DisallowUnknownFields()
 if dec.Decode(&req)!=nil { os.Exit(21) }
 if os.Getenv("SPARKWING_TOKEN")!="" || os.Getenv("AMBIENT_INSTALL_TEST")!="" { os.Exit(22) }
 if err:=os.WriteFile(os.Getenv("INSTALL_MARKER"),[]byte(req.SDK),0600);err!=nil { os.Exit(23) }
 if os.Getenv("INSTALL_FAILURE")!="" { fmt.Fprintln(os.Stderr,os.Getenv("HTTPS_PROXY"));os.Exit(24) }
 if reply:=os.Getenv("INSTALL_REPLY");reply!="" { fmt.Print(reply);return }
 dir:=filepath.Join(req.Directory,req.PipelineDir)
 json.NewEncoder(os.Stdout).Encode(map[string]any{"directory":dir,"command":[]string{filepath.Join(dir,"pipeline")}})
}`

func TestInstallSDKUsesSelectedVerifiedEngineAndFilteredStdin(t *testing.T) {
	key := testSigningKey(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	version := "v0.20.0"
	bundle := signedBundle(t, key, version, target, fixtureExecutableSource(t, version, target, installerFixtureSource))
	release, err := VerifyRelease(version, target, bundle)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(Config{CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AMBIENT_INSTALL_TEST", "must-not-inherit")
	for _, sdk := range []SDKKind{GoSDK, TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			checkout := t.TempDir()
			pipeline := filepath.Join(checkout, ".sparkwing")
			if err := os.Mkdir(pipeline, 0700); err != nil {
				t.Fatal(err)
			}
			writeInstallPin(t, pipeline, sdk)
			pin, err := ReadPin(pipeline, sdk)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolver.StoreVerified(t.Context(), pin, release)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(checkout, "installed")
			request := SDKInstallRequest{Directory: checkout, PipelineDir: ".sparkwing", SDK: sdk, Environment: []string{"INSTALL_MARKER=" + marker, "SPARKWING_TOKEN=private-credential"}}
			command, err := resolved.InstallSDK(t.Context(), request)
			if err != nil || command.Directory != pipeline || len(command.Command) != 1 || command.Command[0] != filepath.Join(pipeline, "pipeline") {
				t.Fatalf("install = %+v, %v", command, err)
			}
			body, err := os.ReadFile(marker)
			if err != nil || string(body) != string(sdk) {
				t.Fatalf("selected installer did not run: %s, %v", body, err)
			}
			for _, reply := range []string{"null", `{}`, `{"directory":"a","directory":"b"}`, `{} {}`, `{"directory":"` + filepath.ToSlash(pipeline) + `","command":[]}`} {
				bad := request
				bad.Environment = append(append([]string{}, request.Environment...), "INSTALL_REPLY="+reply)
				if _, err := resolved.InstallSDK(t.Context(), bad); err == nil {
					t.Fatalf("accepted invalid installer result %q", reply)
				}
			}
			secret := "proxy-password-for-install"
			bad := request
			bad.Environment = append(append([]string{}, request.Environment...), "HTTPS_PROXY=https://user:"+secret+"@proxy.example", "INSTALL_FAILURE=1")
			if _, err := resolved.InstallSDK(t.Context(), bad); err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("installer failure was hidden or exposed proxy credentials: %v", err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, pipeline, pin.Manifest, []byte("changed"))
			if _, err := resolved.InstallSDK(t.Context(), request); err == nil {
				t.Fatal("changed pin reached selected installer")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("invalid pin executed code")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Resolved{}).InstallSDK(ctx, SDKInstallRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled install = %v", err)
	}
}

func writeInstallPin(t *testing.T, dir string, sdk SDKKind) {
	t.Helper()
	if sdk == GoSDK {
		writeFixture(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire "+GoModule+" v0.20.0\n"))
		return
	}
	writeFixture(t, dir, "package.json", []byte(`{"dependencies":{"@sparkwing/sdk":"0.20.0"}}`))
	writeFixture(t, dir, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"0.20.0"}},"node_modules/@sparkwing/sdk":{"version":"0.20.0"}}}`))
}
