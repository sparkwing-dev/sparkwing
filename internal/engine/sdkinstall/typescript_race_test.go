//go:build !windows

package sdkinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestTypeScriptInstallKeepsRunningSDKImportsAvailable(t *testing.T) {
	for _, lockfile := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		t.Run(lockfile, func(t *testing.T) { testTypeScriptInstallKeepsRunningSDKImportsAvailable(t, lockfile) })
	}
}

func testTypeScriptInstallKeepsRunningSDKImportsAvailable(t *testing.T, lockfile string) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	sdk, dir, bin := filepath.Join(root, "sdk"), filepath.Join(root, "pipeline"), filepath.Join(root, "bin")
	for _, path := range []string{sdk, dir, bin} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, sdk, "package.json", []byte(`{"name":"@sparkwing/sdk","version":"0.68.0-qa.16","type":"module","exports":"./index.js"}`))
	writeInput(t, sdk, "index.js", []byte(`export const value = "ready";`))
	writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","dependencies":{"@sparkwing/sdk":"file:../sdk"}}`))
	writeInput(t, dir, "main.mjs", []byte(`const {value} = await import("@sparkwing/sdk"); console.log(value);`))
	lock := exec.CommandContext(t.Context(), npm, "install", "--package-lock-only", "--offline", "--ignore-scripts", "--no-audit", "--no-fund")
	lock.Dir = dir
	if output, err := lock.CombinedOutput(); err != nil {
		t.Fatalf("lock: %v: %s", err, output)
	}
	if lockfile != "package-lock.json" {
		if err := os.Rename(filepath.Join(dir, "package-lock.json"), filepath.Join(dir, lockfile)); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, dir, ".npmrc", []byte("loglevel=verbose\n"))
	// bug: pause npm after its destructive cleanup so an already prepared SDK imports in that window.
	wrapper := fmt.Sprintf(`#!%s
const fs = require("node:fs");
const net = require("node:net");
const {spawnSync} = require("node:child_process");
function install() {
  fs.mkdirSync("npm-active");
  const result = spawnSync(%q, process.argv.slice(2), {stdio:"inherit"});
  fs.rmdirSync("npm-active");
  process.exit(result.status ?? 1);
}
if (["ci", "install"].includes(process.argv[2]) && !process.argv.includes("--dry-run") && fs.existsSync("install-gate")) {
  if (process.argv[2] === "ci") fs.rmSync("node_modules", {recursive:true, force:true});
  const socket = net.connect(JSON.parse(fs.readFileSync("install-gate", "utf8")), "127.0.0.1");
  socket.once("data", () => { socket.destroy(); install(); });
} else install();
`, node, npm)
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: dir, SDK: engineid.TypeScriptSDK})
	if err != nil {
		t.Fatal(err)
	}
	install := func() (engine.SDKCommand, error) {
		var output, diagnostics bytes.Buffer
		err := Run(t.Context(), "v0.68.0-qa.16", bytes.NewReader(request), &output, &diagnostics)
		var command engine.SDKCommand
		if err == nil {
			err = json.Unmarshal(output.Bytes(), &command)
		}
		return command, err
	}
	first, err := install()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	writeInput(t, dir, "install-gate", fmt.Appendf(nil, "%d", port))
	entered := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		entered <- connection
	}()
	done := make(chan error, 1)
	go func() { _, err := install(); done <- err }()
	var gate net.Conn
	select {
	case gate = <-entered:
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	output, importErr := exec.CommandContext(t.Context(), first.Command[0], first.Command[1:]...).CombinedOutput()
	if gate != nil {
		_, _ = gate.Write([]byte("continue"))
		_ = gate.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if importErr != nil || string(output) != "ready\n" {
		t.Fatalf("prepared SDK import during another installation: %v: %s", importErr, output)
	}
	if err := os.Remove(filepath.Join(dir, "install-gate")); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	for range 4 {
		go func() { <-started; _, err := install(); done <- err }()
	}
	close(started)
	for range 4 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent SDK preparation: %v", err)
		}
	}
	writeInput(t, dir, "package.json", []byte(`{"main":"main.mjs","dependencies":{"@sparkwing/sdk":"file:../sdk"},"scripts":{"postinstall":"node -e \"require('node:fs').writeFileSync('install-receipt', 'changed')\""}}`))
	if _, err := install(); err != nil {
		t.Fatal(err)
	}
	if receipt, err := os.ReadFile(filepath.Join(dir, "install-receipt")); err != nil || string(receipt) != "changed" {
		t.Fatalf("changed manifest did not install: %s: %v", receipt, err)
	}
	if err := os.Remove(filepath.Join(dir, "install-receipt")); err != nil {
		t.Fatal(err)
	}
	writeInput(t, dir, ".npmrc", []byte("ignore-scripts=true\n"))
	if _, err := install(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "install-receipt")); !os.IsNotExist(err) {
		t.Fatalf("npm ignored project configuration: %v", err)
	}
	writeInput(t, dir, ".npmrc", []byte("ignore-scripts=false\n"))
	if _, err := install(); err != nil {
		t.Fatal(err)
	}
	if receipt, err := os.ReadFile(filepath.Join(dir, "install-receipt")); err != nil || string(receipt) != "changed" {
		t.Fatalf("changed npm configuration did not install: %s: %v", receipt, err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, err := install(); err != nil {
		t.Fatal(err)
	}
	output, err = exec.CommandContext(t.Context(), first.Command[0], first.Command[1:]...).CombinedOutput()
	if err != nil || string(output) != "ready\n" {
		t.Fatalf("SDK import after removing node_modules: %v: %s", err, output)
	}
}
