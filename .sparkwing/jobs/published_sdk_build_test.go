package jobs

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func publishedSDKFixture(t *testing.T) string {
	t.Helper()
	root := gateFixtureRepo(t)
	configurePublishedSDKFixture(t, root)
	gitCommitAll(t, root, "published API")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	return root
}

func configurePublishedSDKFixture(t *testing.T, root string) {
	t.Helper()
	t.Chdir(root)
	const version = "v0.1.0"
	mod := "module " + sdkModulePath + "\n\ngo 1.26.0\n"
	proxy := t.TempDir()
	versions := filepath.Join(proxy, filepath.FromSlash(sdkModulePath), "@v")
	writeGoFile(t, filepath.Join(versions, version+".mod"), mod)
	writeGoFile(t, filepath.Join(versions, version+".info"), `{"Version":"v0.1.0","Time":"2026-11-01T00:00:00Z"}`)
	archive, err := os.Create(filepath.Join(versions, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, body := range map[string]string{"go.mod": mod, "sdk.go": "package sparkwing\ntype ExecDone struct { ExitCode int }\n"} {
		entry, err := writer.Create(sdkModulePath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", (&url.URL{Scheme: "file", Path: filepath.ToSlash(proxy)}).String())
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-modcacherw")
	writeGoFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.26.0\nrequire "+sdkModulePath+" "+version+"\n")
	writeGoFile(t, filepath.Join(root, "sparkwing/go.mod"), mod)
	writeGoFile(t, filepath.Join(root, "sparkwing/sdk.go"), "package sparkwing\ntype ExecDone struct { ExitCode int; CleanupError string }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing/go.mod"), "module fixture-pipelines\n\ngo 1.26.0\n")
	for _, name := range []string{"sparkwing", "sparkwing-controller", "sparkwing-runner"} {
		writeGoFile(t, filepath.Join(root, "cmd", name, "main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.ExitCode }\n")
	}
	if out, err := exec.Command("go", "mod", "download", sdkModulePath).CombinedOutput(); err != nil {
		t.Fatalf("seed private published module: %v\n%s", err, out)
	}
	writeGoFile(t, filepath.Join(root, "go.work"), "go 1.26.0\nuse (\n.\n./sparkwing\n./.sparkwing\n)\n")
	t.Setenv("GOWORK", "auto")
}

func TestPublishedSDKBuildRejectsWorkspaceOnlyAPIOnEveryLandPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  sparkwing.Workable
	}{
		{"full", &Gate{mode: gateFull}}, {"affected", &Gate{mode: gateAffected}}, {"touched", &PrePush{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := publishedSDKFixture(t)
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.CleanupError }\n")
			gitCommitAll(t, root, "unpublished API")
			if out, err := exec.Command("go", "build", "-o", os.DevNull, "./cmd/...").CombinedOutput(); err != nil {
				t.Fatalf("workspace control: %v\n%s", err, out)
			}
			if err := runPublishedSDKJobStep(t, tc.job); err == nil || !strings.Contains(err.Error(), "CleanupError") {
				t.Fatalf("published check must reject the unavailable API: %v", err)
			}
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.ExitCode }\n")
			if err := runPublishedSDKJobStep(t, tc.job); err != nil {
				t.Fatalf("published API must pass: %v", err)
			}
		})
	}
}

func runPublishedSDKJobStep(t *testing.T, job sparkwing.Workable) error {
	t.Helper()
	host, plan, work := prepareWork(t, job)
	if findStep(work, "build-published-sdk") == nil {
		t.Fatal("land build has no published-SDK check")
	}
	prerequisites := []string{"vet", "build"}
	if _, ok := job.(*PrePush); ok {
		prerequisites = []string{"build-touched"}
	}
	for _, step := range prerequisites {
		if _, err := host.RunStep(t.Context(), plan, "job", step); err != nil {
			t.Fatalf("workspace prerequisite %s: %v", step, err)
		}
	}
	_, err := host.RunStep(t.Context(), plan, "job", "build-published-sdk")
	return err
}

func TestPublishedSDKBuildKeepsTheExistingCompilerAllowance(t *testing.T) {
	for _, job := range []sparkwing.Workable{&Gate{mode: gateFull}, &Gate{mode: gateAffected}, &PrePush{}} {
		work := inspectWork(t, job)
		step := findStep(work, "build-published-sdk")
		if step == nil {
			t.Fatal("missing published-SDK build")
		}
		prior := "build"
		if _, ok := job.(*PrePush); ok {
			prior = "build-touched"
		}
		if len(step.Needs) != 1 || step.Needs[0] != prior {
			t.Fatalf("published build needs %v, want [%s]", step.Needs, prior)
		}
	}
}

func TestPublishedSDKBuildRefusesAReplacement(t *testing.T) {
	root := publishedSDKFixture(t)
	file := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, file, string(data)+"replace "+sdkModulePath+" => ./sparkwing\n")
	if err := runPublishedSDKBuild(newTestContext(t)); err == nil || !strings.Contains(err.Error(), "without replace") {
		t.Fatalf("replacement passed: %v", err)
	}
}

func TestPublishedSDKBuildIgnoresAmbientModuleAndVendorOverrides(t *testing.T) {
	for _, override := range []string{"GOFLAGS", "GOENV", "vendor"} {
		t.Run(override, func(t *testing.T) {
			root := publishedSDKFixture(t)
			bad := "package main\nimport sdk \"" + sdkModulePath + "\"\nfunc main() { _ = sdk.ExecDone{}.CleanupError }\n"
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), bad)
			mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			alternate := filepath.Join(root, "alternate.mod")
			writeGoFile(t, alternate, string(mod)+"replace "+sdkModulePath+" => ./sparkwing\n")
			switch override {
			case "GOFLAGS":
				t.Setenv("GOFLAGS", "-modfile="+alternate)
			case "GOENV":
				goenv := filepath.Join(t.TempDir(), "goenv")
				writeGoFile(t, goenv, "GOFLAGS=-modfile="+alternate+"\n")
				t.Setenv("GOFLAGS", "")
				if err := os.Unsetenv("GOFLAGS"); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GOENV", goenv)
			case "vendor":
				writeGoFile(t, filepath.Join(root, "vendor/modules.txt"), "# "+sdkModulePath+" v0.1.0\n## explicit; go 1.26.0\n"+sdkModulePath+"\n")
				writeGoFile(t, filepath.Join(root, "vendor", filepath.FromSlash(sdkModulePath), "sdk.go"), "package sparkwing\ntype ExecDone struct { ExitCode int; CleanupError string }\n")
			}
			if err := runPublishedSDKBuild(newTestContext(t)); err == nil || !strings.Contains(err.Error(), "CleanupError") {
				t.Fatalf("%s masked unpublished API: %v", override, err)
			}
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.ExitCode }\n")
			if err := runPublishedSDKBuild(newTestContext(t)); err != nil {
				t.Fatalf("published API with %s override: %v", override, err)
			}
		})
	}
}

func TestPublishedSDKBuildUsesReleaseCGOMode(t *testing.T) {
	root := publishedSDKFixture(t)
	t.Setenv("CGO_ENABLED", "1")
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing/cgo.go"), "//go:build cgo\n\npackage main\nvar _ = releaseMustDisableCGO\n")
	if err := runPublishedSDKBuild(newTestContext(t)); err != nil {
		t.Fatalf("release build enabled cgo: %v", err)
	}
}

func TestPublishedSDKBuildIncludesSDKOnlyAndEmbeddedInputChanges(t *testing.T) {
	root := publishedSDKFixture(t)
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.CleanupError }\n")
	for _, file := range []string{"sparkwing/sdk.go", "go.mod", "go.sum", "internal/input.txt"} {
		if file == "internal/input.txt" {
			writeGoFile(t, filepath.Join(root, file), "embedded input\n")
		}
		if err := runPublishedSDKBuildFiles(newTestContext(t), []string{file}, 1); err == nil || !strings.Contains(err.Error(), "CleanupError") {
			t.Fatalf("%s skipped root release build: %v", file, err)
		}
	}
	if err := runPublishedSDKBuildFiles(newTestContext(t), []string{"docs/guide.md"}, 1); err != nil {
		t.Fatalf("docs-only change built broken unrelated commands: %v", err)
	}
}

func TestPublishedSDKBuildRejectsAnUnpublishedMergeDependency(t *testing.T) {
	root := publishedSDKFixture(t)
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.CleanupError }\n")
	gitCommitAll(t, root, "branch uses local SDK")
	if err := recheckMergeDelta(newTestContext(t), []string{"sparkwing/sdk.go"}); err == nil || !strings.Contains(err.Error(), "CleanupError") {
		t.Fatalf("merge recheck accepted unpublished root API: %v", err)
	}
}

func TestPublishedSDKBuildRejectsTargetSpecificUnpublishedAPIs(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		t.Run(platform, func(t *testing.T) {
			root := publishedSDKFixture(t)
			t.Setenv("GOOS", "linux")
			t.Setenv("GOARCH", "amd64")
			goos, goarch, _ := strings.Cut(platform, "/")
			file := filepath.Join(root, "cmd/sparkwing", "extra_"+goos+"_"+goarch+".go")
			writeGoFile(t, file, "package main\nimport sdk \""+sdkModulePath+"\"\nvar _ = sdk.ExecDone{}.CleanupError\n")
			if err := buildPublishedSDK(newTestContext(t), 1, gateFull); err == nil || !strings.Contains(err.Error(), "CleanupError") {
				t.Fatalf("%s unpublished API escaped the release check: %v", platform, err)
			}
		})
	}
}

func TestPublishedSDKBuildMatchesReleaseBinarySet(t *testing.T) {
	root := publishedSDKFixture(t)
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing-controller/extra_windows.go"), "package main\nvar _ = controllerIsNotReleasedOnWindows\n")
	if err := buildPublishedSDK(newTestContext(t), 1, gateFull); err != nil {
		t.Fatalf("release check included an unsupported binary: %v", err)
	}
}

func TestPublishedSDKBuildSkipsUnownedDocsOnAffectedGate(t *testing.T) {
	root := publishedSDKFixture(t)
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nfunc main() { _ = sdk.ExecDone{}.CleanupError }\n")
	gitCommitAll(t, root, "unrelated broken command")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "docs/guide.md"), "# Ordinary documentation\n")
	gitCommitAll(t, root, "documentation")
	host, plan, work := prepareWork(t, &Gate{mode: gateAffected})
	for _, step := range []string{"vet", "build"} {
		if _, err := host.RunStep(t.Context(), plan, "job", step); err != nil {
			t.Fatal(err)
		}
	}
	closures := findStep(work, "build-published-sdk").Closures.SkipIf
	if len(closures) == 0 {
		t.Fatal("affected published build has no scope predicate")
	}
	for _, closure := range closures {
		raw, err := host.Eval(t.Context(), plan, "job", closure, nil)
		if err != nil {
			t.Fatal(err)
		}
		var skip bool
		if err := json.Unmarshal(raw, &skip); err != nil {
			t.Fatal(err)
		}
		if !skip {
			t.Fatal("ordinary documentation selected an unrelated release build")
		}
	}
}

func TestPublishedSDKBuildIncludesDeletedMarkdownEmbedsOnEveryLandPath(t *testing.T) {
	for _, rel := range []string{"message.md", "assets/message.md"} {
		for _, tc := range []struct {
			name string
			job  sparkwing.Workable
		}{{"affected", &Gate{mode: gateAffected}}, {"touched", &PrePush{}}, {"merge", nil}} {
			t.Run(rel+"/"+tc.name, func(t *testing.T) {
				root := publishedSDKFixture(t)
				writeGoFile(t, filepath.Join(root, "cmd/sparkwing/main.go"), fmt.Sprintf("package main\nimport _ \"embed\"\n//go:embed %s\nvar message string\nfunc main() {}\n", rel))
				asset := filepath.Join(root, "cmd/sparkwing", rel)
				writeGoFile(t, asset, "embedded Markdown\n")
				gitCommitAll(t, root, "embed Markdown")
				runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
				check := func() error { return runPublishedSDKBuildFiles(newTestContext(t), []string{"cmd/sparkwing/" + rel}, 1) }
				if tc.job != nil {
					host, plan, work := prepareWork(t, tc.job)
					prerequisites := []string{"vet", "build"}
					if _, ok := tc.job.(*PrePush); ok {
						prerequisites = []string{"build-touched"}
					}
					for _, step := range prerequisites {
						if _, err := host.RunStep(t.Context(), plan, "job", step); err != nil {
							t.Fatal(err)
						}
					}
					check = func() error {
						for _, closure := range findStep(work, "build-published-sdk").Closures.SkipIf {
							raw, err := host.Eval(t.Context(), plan, "job", closure, nil)
							if err != nil {
								return err
							}
							var skip bool
							if err := json.Unmarshal(raw, &skip); err != nil {
								return err
							}
							if skip {
								return fmt.Errorf("%s skipped deleted embedded input", tc.name)
							}
						}
						_, err := host.RunStep(t.Context(), plan, "job", "build-published-sdk")
						return err
					}
				}
				if err := os.Remove(asset); err != nil {
					t.Fatal(err)
				}
				gitCommitAll(t, root, "remove embedded input")
				if out, err := exec.Command("go", "build", "-o", os.DevNull, "./cmd/...").CombinedOutput(); err == nil || !strings.Contains(string(out), "no matching files found") {
					t.Fatalf("deleted embed control: %s %v", out, err)
				}
				if err := check(); err == nil || !strings.Contains(err.Error(), "no matching files found") {
					t.Fatalf("deleted Markdown embed escaped %s: %v", tc.name, err)
				}
			})
		}
	}
}

func TestPublishedSDKBuildIncludesPlatformOnlyMarkdownInputs(t *testing.T) {
	root := publishedSDKFixture(t)
	writeGoFile(t, filepath.Join(root, "internal/windowsdata/data_windows.go"), "package windowsdata\nimport _ \"embed\"\n//go:embed message.md\nvar Message string\n")
	writeGoFile(t, filepath.Join(root, "cmd/sparkwing/extra_windows.go"), "package main\nimport _ \"fixture/internal/windowsdata\"\n")
	asset := filepath.Join(root, "internal/windowsdata/message.md")
	writeGoFile(t, asset, "Windows input\n")
	gitCommitAll(t, root, "Windows-only package")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.Remove(asset); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root, "delete Windows input")
	if why, err := publishedSDKBuildChange(newTestContext(t), []string{"internal/windowsdata/message.md"}); err != nil || why == "" {
		t.Fatalf("platform-only Markdown not selected: %s %v", why, err)
	}
	if runtime.GOOS != "windows" {
		if err := runPublishedSDKBuildTouched(newTestContext(t)); err != nil {
			t.Fatalf("affected tier built deferred Windows input: %v", err)
		}
	}
	if err := buildPublishedSDK(newTestContext(t), 1, gateFull); err == nil || !strings.Contains(err.Error(), "no matching files found") {
		t.Fatalf("platform-only embed escaped release build: %v", err)
	}
}

func TestPublishedSDKBuildDefersOtherTargetsToFullGate(t *testing.T) {
	deferred := "darwin/arm64"
	if runtime.GOOS+"/"+runtime.GOARCH == deferred {
		deferred = "windows/amd64"
	}
	goos, goarch, _ := strings.Cut(deferred, "/")
	for _, tc := range []struct {
		name string
		job  sparkwing.Workable
		full bool
	}{
		{"full", &Gate{mode: gateFull}, true}, {"affected", &Gate{mode: gateAffected}, false}, {"touched", &PrePush{}, false}, {"merge", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := publishedSDKFixture(t)
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing", "extra_"+goos+"_"+goarch+".go"), "package main\nimport sdk \""+sdkModulePath+"\"\nvar _ = sdk.ExecDone{}.CleanupError\n")
			gitCommitAll(t, root, "target-specific API")
			var err error
			if tc.job == nil {
				err = runPublishedSDKBuildFiles(newTestContext(t), []string{"cmd/sparkwing/extra_" + goos + "_" + goarch + ".go"}, 1)
			} else {
				err = runPublishedSDKJobStep(t, tc.job)
			}
			if tc.full {
				if err == nil || !strings.Contains(err.Error(), "CleanupError") {
					t.Fatalf("full tier missed %s: %v", deferred, err)
				}
			} else if err != nil {
				t.Fatalf("affected tier built deferred %s: %v", deferred, err)
			}
		})
	}
}

func TestPublishedSDKBuildKeepsDeployARM64OnAffectedPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  sparkwing.Workable
	}{{"affected", &Gate{mode: gateAffected}}, {"touched", &PrePush{}}, {"merge", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			root := publishedSDKFixture(t)
			writeGoFile(t, filepath.Join(root, "cmd/sparkwing/extra_linux_arm64.go"), "package main\nimport sdk \""+sdkModulePath+"\"\nvar _ = sdk.ExecDone{}.CleanupError\n")
			gitCommitAll(t, root, "deploy API")
			var err error
			if tc.job == nil {
				err = runPublishedSDKBuildFiles(newTestContext(t), []string{"cmd/sparkwing/extra_linux_arm64.go"}, 3)
			} else {
				err = runPublishedSDKJobStep(t, tc.job)
			}
			if err == nil || !strings.Contains(err.Error(), "CleanupError") {
				t.Fatalf("deploy arm64 API escaped: %v", err)
			}
		})
	}
}

func TestPublishedSDKBuildAvoidsDuplicateDeployTarget(t *testing.T) {
	targets := publishedSDKPlatforms(gateAffected, "linux/arm64")
	if len(targets) != 1 || targets[0] != "linux/arm64" {
		t.Fatalf("duplicate host/deploy target: %v", targets)
	}
}

func TestPublishedSDKBuildSharesConcurrentTargetAllowance(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("host and deploy are one target")
	}
	for _, cores := range []int{1, 3} {
		t.Run(strconv.Itoa(cores), func(t *testing.T) {
			publishedSDKFixture(t)
			realGo, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var active, peak, starts int
			var invalid string
			release := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				allowance, _ := strconv.Atoi(r.URL.Query().Get("cores"))
				packages, _ := strconv.Atoi(r.URL.Query().Get("packages"))
				mu.Lock()
				starts++
				active += allowance
				peak = max(peak, active)
				if allowance != packages || allowance < 1 {
					invalid = r.URL.RawQuery
				}
				if cores == 1 || starts == 2 {
					once.Do(func() { close(release) })
				}
				mu.Unlock()
				select {
				case <-release:
				case <-r.Context().Done():
				}
				mu.Lock()
				active -= allowance
				mu.Unlock()
			}))
			defer server.Close()
			tools := t.TempDir()
			writeDeliveryTool(t, filepath.Join(tools, "go"), "#!/bin/sh\nif [ \"$1\" = build ]; then exec curl --fail --silent \""+server.URL+"/$GOOS/$GOARCH?cores=$GOMAXPROCS&packages=$3\"; fi\nexec "+shellQuote(realGo)+" \"$@\"\n")
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(newTestContext(t), 10*time.Second)
			defer cancel()
			err = runPublishedSDKBuildFiles(ctx, []string{"go.mod"}, cores)
			once.Do(func() { close(release) })
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if starts != 2 || peak != cores || invalid != "" {
				t.Fatalf("starts=%d peak=%d allowance=%d invalid=%q", starts, peak, cores, invalid)
			}
		})
	}
}
