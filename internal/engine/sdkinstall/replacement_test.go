package sdkinstall

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestRunGoSDKReplacementBuildsWithInstalledEngine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "home"))
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	dir, sdk := filepath.Join(root, "pipeline"), filepath.Join(root, "sdk")
	for _, path := range []string{dir, sdk} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, sdk, "go.mod", []byte("module "+engine.GoModule+"\ngo 1.26\n"))
	writeInput(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire "+engine.GoModule+" v0.0.0\nreplace "+engine.GoModule+" => ../sdk\n"))
	writeInput(t, dir, "main.go", []byte("package main\nimport (\"fmt\"; sdk \""+engine.GoModule+"\")\nfunc main(){fmt.Print(sdk.Value)}\n"))
	for _, value := range []string{"first", "changed replacement"} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		writeInput(t, sdk, "sdk.go", append([]byte("package sdk\nconst Value="), body...))
		request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engine.GoSDK, ExecutableDirectory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		var output, diagnostics bytes.Buffer
		if err := Run(t.Context(), "installed-version", bytes.NewReader(request), &output, &diagnostics); err != nil {
			t.Fatalf("replacement installation: %v: %s", err, &diagnostics)
		}
		var command engine.SDKCommand
		if err := json.Unmarshal(output.Bytes(), &command); err != nil {
			t.Fatal(err)
		}
		got, err := exec.CommandContext(t.Context(), command.Command[0]).CombinedOutput()
		if err != nil || string(got) != value {
			t.Fatalf("replacement execution: %q want %q: %v", got, value, err)
		}
	}
}
