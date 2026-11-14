//go:build !windows

package sdkinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

type receiptFixture struct {
	t                               *testing.T
	root, dir, bin, npmPackage, log string
	request                         []byte
}

const receiptManifest = `{"main":"index.ts","dependencies":{"@sparkwing/sdk":"0.20.0","extra":"file:extra.tgz"}}`

const receiptLockfile = `{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"0.20.0","extra":"file:extra.tgz"}},"node_modules/@sparkwing/sdk":{"version":"0.20.0"},"node_modules/extra":{"version":"1.0.0","resolved":"file:extra.tgz"}}}`

func newReceiptFixture(t *testing.T) *receiptFixture {
	t.Helper()
	root := t.TempDir()
	f := &receiptFixture{t: t, root: root, dir: filepath.Join(root, "repo", "pipeline"), bin: filepath.Join(root, "bin"), npmPackage: filepath.Join(root, "npm"), log: filepath.Join(root, "npm.log")}
	for _, path := range []string{f.dir, f.bin, filepath.Join(f.npmPackage, "bin"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.write(filepath.Join(f.dir, "package.json"), receiptManifest)
	f.write(filepath.Join(f.dir, "package-lock.json"), receiptLockfile)
	f.write(filepath.Join(f.dir, "index.ts"), "export {}")
	f.write(filepath.Join(f.dir, "extra.tgz"), "tarball")
	f.write(filepath.Join(f.npmPackage, "package.json"), `{"name":"npm","version":"11.0.0"}`)
	f.writeNPM("")
	f.writeExecutable(filepath.Join(f.bin, "node"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", filepath.Join(f.npmPackage, "bin")+string(os.PathListSeparator)+f.bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", filepath.Join(root, "home"))
	for _, name := range []string{"npm_config_userconfig", "npm_config_globalconfig", "npm_config_prefix", "PREFIX", "NODE_ENV", "NODE_OPTIONS", "NODE_PATH"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: f.dir, SDK: engineid.TypeScriptSDK})
	if err != nil {
		t.Fatal(err)
	}
	f.request = request
	return f
}

func (f *receiptFixture) write(path, body string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *receiptFixture) writeExecutable(path, body string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *receiptFixture) writeNPM(extra string) {
	f.writeExecutable(filepath.Join(f.npmPackage, "bin", "npm"), fmt.Sprintf(`#!/bin/sh
echo "$1" >> %q
%s
case "$1" in
ci) exit 0 ;;
install)
  mkdir -p node_modules/@sparkwing/sdk
  printf '{"version":"0.20.0"}' > node_modules/@sparkwing/sdk/package.json
  printf '{"lockfileVersion":3}' > node_modules/.package-lock.json
  if [ -e npm-fail ]; then exit 1; fi
  exit 0 ;;
esac
exit 10
`, f.log, extra))
}

func (f *receiptFixture) install() error {
	f.t.Helper()
	var output, diagnostics bytes.Buffer
	err := Run(f.t.Context(), bytes.NewReader(f.request), &output, &diagnostics)
	if err != nil {
		return fmt.Errorf("%w: %s", err, &diagnostics)
	}
	var command engine.SDKCommand
	if err := json.Unmarshal(output.Bytes(), &command); err != nil {
		return err
	}
	if len(command.Command) != 2 || command.Command[1] != filepath.Join(f.dir, "index.ts") {
		return fmt.Errorf("unexpected SDK command %q", command.Command)
	}
	return nil
}

func (f *receiptFixture) npmRuns() int {
	f.t.Helper()
	body, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Count(string(body), "\n")
}

func (f *receiptFixture) mustInstall() bool {
	f.t.Helper()
	before := f.npmRuns()
	if err := f.install(); err != nil {
		f.t.Fatal(err)
	}
	ran := f.npmRuns() - before
	if ran != 0 && ran != 2 {
		f.t.Fatalf("npm ran %d times; preparation runs ci --dry-run and install together", ran)
	}
	return ran == 2
}

func TestTypeScriptInstallReusesAnUnchangedVerifiedTree(t *testing.T) {
	f := newReceiptFixture(t)
	if !f.mustInstall() {
		t.Fatal("first preparation skipped npm")
	}
	if f.mustInstall() {
		t.Fatal("unchanged verified tree ran npm again")
	}
	if f.mustInstall() {
		t.Fatal("unchanged verified tree ran npm on its third preparation")
	}
}

func TestTypeScriptInstallReceiptCoversEveryNPMInput(t *testing.T) {
	for _, input := range []struct {
		name   string
		change func(*receiptFixture)
	}{
		{"manifest", func(f *receiptFixture) {
			f.write(filepath.Join(f.dir, "package.json"), strings.Replace(receiptManifest, `{"main"`, `{"description":"changed","main"`, 1))
		}},
		{"lockfile", func(f *receiptFixture) { f.write(filepath.Join(f.dir, "package-lock.json"), receiptLockfile+"\n") }},
		{"npm tree record", func(f *receiptFixture) {
			f.write(filepath.Join(f.dir, "node_modules", ".package-lock.json"), `{"lockfileVersion":3,"packages":{}}`)
		}},
		{"installed SDK manifest", func(f *receiptFixture) {
			f.write(filepath.Join(f.dir, "node_modules", "@sparkwing", "sdk", "package.json"), `{"version":"0.20.0","edited":true}`)
		}},
		{"project npmrc", func(f *receiptFixture) { f.write(filepath.Join(f.dir, ".npmrc"), "omit=dev\n") }},
		{"ancestor npmrc", func(f *receiptFixture) { f.write(filepath.Join(f.root, "repo", ".npmrc"), "omit=dev\n") }},
		{"ancestor workspace manifest", func(f *receiptFixture) {
			f.write(filepath.Join(f.root, "repo", "package.json"), `{"private":true,"workspaces":["pipeline"]}`)
		}},
		{"ancestor workspace lockfile", func(f *receiptFixture) {
			f.write(filepath.Join(f.root, "repo", "package-lock.json"), `{"lockfileVersion":3,"packages":{}}`)
		}},
		{"user npmrc", func(f *receiptFixture) { f.write(filepath.Join(f.root, "home", ".npmrc"), "omit=dev\n") }},
		{"selected user npmrc", func(f *receiptFixture) {
			path := filepath.Join(f.root, "user.npmrc")
			t.Setenv("npm_config_userconfig", path)
			f.write(path, "omit=dev\n")
		}},
		{"default global npmrc", func(f *receiptFixture) {
			if err := os.MkdirAll(filepath.Join(f.root, "etc"), 0o700); err != nil {
				f.t.Fatal(err)
			}
			f.write(filepath.Join(f.root, "etc", "npmrc"), "omit=dev\n")
		}},
		{"selected global npmrc", func(f *receiptFixture) {
			path := filepath.Join(f.root, "global.npmrc")
			t.Setenv("npm_config_globalconfig", path)
			f.write(path, "omit=dev\n")
		}},
		{"builtin npmrc", func(f *receiptFixture) { f.write(filepath.Join(f.npmPackage, "npmrc"), "omit=dev\n") }},
		{"npm package version", func(f *receiptFixture) {
			f.write(filepath.Join(f.npmPackage, "package.json"), `{"name":"npm","version":"11.0.1"}`)
		}},
		{"npm configuration environment", func(f *receiptFixture) { t.Setenv("npm_config_omit", "dev") }},
		{"NODE_ENV", func(f *receiptFixture) { t.Setenv("NODE_ENV", "production") }},
		{"node executable", func(f *receiptFixture) {
			f.writeExecutable(filepath.Join(f.bin, "node"), "#!/bin/sh\n# upgraded\nexit 0\n")
		}},
		{"npm executable", func(f *receiptFixture) { f.writeNPM("# upgraded") }},
		{"local tarball dependency", func(f *receiptFixture) { f.write(filepath.Join(f.dir, "extra.tgz"), "rebuilt tarball") }},
		{"missing receipt", func(f *receiptFixture) {
			if err := os.Remove(filepath.Join(f.dir, "node_modules", typeScriptReceipt)); err != nil {
				f.t.Fatal(err)
			}
		}},
	} {
		t.Run(input.name, func(t *testing.T) {
			f := newReceiptFixture(t)
			f.mustInstall()
			if f.mustInstall() {
				t.Fatal("unchanged verified tree ran npm again")
			}
			input.change(f)
			if !f.mustInstall() {
				t.Fatalf("changing the %s reused the previous install", input.name)
			}
			if f.mustInstall() {
				t.Fatal("the reinstalled tree was not recorded for reuse")
			}
		})
	}
}

func TestTypeScriptInstallNeverReusesRootLifecycleScripts(t *testing.T) {
	for _, script := range typeScriptLifecycleScripts {
		t.Run(script, func(t *testing.T) {
			f := newReceiptFixture(t)
			f.write(filepath.Join(f.dir, "package.json"), strings.Replace(receiptManifest, `{"main"`, fmt.Sprintf(`{"scripts":{%q:"true"},"main"`, script), 1))
			for range 3 {
				if !f.mustInstall() {
					t.Fatalf("root %s script was skipped", script)
				}
			}
		})
	}
}

func TestTypeScriptInstallFailureInvalidatesTheReceiptBeforeNPMWrites(t *testing.T) {
	f := newReceiptFixture(t)
	f.mustInstall()
	if f.mustInstall() {
		t.Fatal("unchanged verified tree ran npm again")
	}
	manifest := filepath.Join(f.dir, "package.json")
	f.write(manifest, strings.Replace(receiptManifest, `{"main"`, `{"description":"changed","main"`, 1))
	f.write(filepath.Join(f.dir, "npm-fail"), "")
	if err := f.install(); err == nil {
		t.Fatal("failed npm install reported success")
	}
	if err := os.Remove(filepath.Join(f.dir, "npm-fail")); err != nil {
		t.Fatal(err)
	}
	f.write(manifest, receiptManifest)
	if !f.mustInstall() {
		t.Fatal("a tree npm failed to finish matched the receipt of the install before it")
	}
}
