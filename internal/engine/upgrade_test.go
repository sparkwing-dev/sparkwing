package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

type verifyFunc func(context.Context, string, Resolved) error

func (f verifyFunc) Verify(ctx context.Context, dir string, resolved Resolved) error {
	return f(ctx, dir, resolved)
}

func TestUpgradeStagesNativePinsAndLocksBeforeApplying(t *testing.T) {
	for _, sdk := range []engineid.SDKKind{engineid.GoSDK, engineid.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			r, transport, key, _, _ := fixtureResolver(t)
			target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
			transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
			repo := t.TempDir()
			createUpgradeRepo(t, repo, sdk)
			application := []byte("application manifest must stay unchanged")
			writeFixture(t, repo, "go.mod", application)
			writeFixture(t, repo, "package.json", application)
			r.command = func(ctx context.Context, stage, name string, args ...string) error {
				if stage == repo {
					t.Fatal("native resolver ran in original checkout")
				}
				if sdk == engineid.GoSDK {
					if name != "go" {
						t.Fatalf("wrong native tool %s", name)
					}
					writeFixture(t, stage, "go.sum", []byte("new native checksums\n"))
				} else {
					if name != "npm" || !strings.Contains(strings.Join(args, " "), "--ignore-scripts") {
						t.Fatalf("unsafe native tool %s %v", name, args)
					}
					writeFixture(t, stage, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"0.20.0"}},"node_modules/@sparkwing/sdk":{"version":"0.20.0"}}}`))
				}
				return nil
			}
			request := UpgradeRequest{Checkout: repo, SDK: sdk, DryRun: true}
			before, err := ReadPin(filepath.Join(repo, ".sparkwing"), sdk)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			verifier := verifyFunc(func(ctx context.Context, stage string, resolved Resolved) error {
				called = true
				if filepath.Base(stage) == ".sparkwing" {
					t.Fatalf("verifier received pipeline instead of checkout root: %s", stage)
				}
				if stage == repo || resolved.Identity.Version != "v0.20.0" {
					t.Fatalf("invalid verification boundary %s %+v", stage, resolved)
				}
				original, err := ReadPin(filepath.Join(repo, ".sparkwing"), sdk)
				if err != nil {
					return err
				}
				if original != before {
					return fmt.Errorf("original changed before verification")
				}
				pin, err := ReadPin(filepath.Join(stage, ".sparkwing"), sdk)
				if err != nil {
					return err
				}
				if pin.Version == before.Version {
					return fmt.Errorf("verification saw original pin")
				}
				return nil
			})
			preview, err := r.Upgrade(context.Background(), request, verifier)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Applied || preview.Verified || called || len(preview.Plan.Edits) != 2 {
				t.Fatalf("invalid dry run: %+v called=%v", preview, called)
			}
			for _, edit := range preview.Plan.Edits {
				if filepath.Dir(edit.Path) != ".sparkwing" {
					t.Fatalf("preview addresses wrong directory: %s", edit.Path)
				}
			}
			actual, err := ReadPin(filepath.Join(repo, ".sparkwing"), sdk)
			if err != nil || actual != before {
				t.Fatalf("dry run edited pin: %+v %v", actual, err)
			}
			request.DryRun = false
			result, err := r.Upgrade(context.Background(), request, verifier)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Applied || !result.Verified || !called {
				t.Fatalf("upgrade did not verify/apply: %+v", result)
			}
			actual, err = ReadPin(filepath.Join(repo, ".sparkwing"), sdk)
			if err != nil || actual != result.Plan.After {
				t.Fatalf("wrong applied pin: %+v %v", actual, err)
			}
			for _, name := range []string{"go.mod", "package.json"} {
				current, err := os.ReadFile(filepath.Join(repo, name))
				if err != nil || string(current) != string(application) {
					t.Fatalf("application manifest changed: %s %v", current, err)
				}
			}
			content, err := os.ReadFile(filepath.Join(repo, "unrelated.txt"))
			if err != nil || string(content) != "keep me" {
				t.Fatal("unrelated source changed")
			}
		})
	}
}

func createUpgradeRepo(t *testing.T, dir string, sdk engineid.SDKKind) {
	t.Helper()
	writeFixture(t, dir, "unrelated.txt", []byte("keep me"))
	dir = filepath.Join(dir, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if sdk == engineid.GoSDK {
		writeFixture(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire github.com/sparkwing-dev/sparkwing v0.10.2\n"))
		writeFixture(t, dir, "go.sum", []byte("original native checksums\n"))
	} else {
		writeFixture(t, dir, "package.json", []byte(`{"name":"fixture","dependencies":{"@sparkwing/sdk":"0.10.2"},"description":"keep me"}`))
		writeFixture(t, dir, "package-lock.json", []byte(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"0.10.2"}},"node_modules/@sparkwing/sdk":{"version":"0.10.2"}}}`))
	}
}

func TestBreakingUpgradeMovesThePinAndReportsTheCodeThatFails(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
	var ran []string
	r.command = func(_ context.Context, stage, name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if args[0] == "mod" {
			return errors.New("package github.com/sparkwing-dev/sparkwing/pkg/runner provided at latest version but not at required version")
		}
		writeFixture(t, stage, "go.sum", []byte("updated checksums"))
		return nil
	}
	repo := t.TempDir()
	createUpgradeRepo(t, repo, engineid.GoSDK)
	removed := fmt.Errorf("%w: upgrade: go build: main.go:5:2: undefined: runner.Main", ErrPipelineIncompatible)
	result, err := r.Upgrade(context.Background(), UpgradeRequest{Checkout: repo, SDK: engineid.GoSDK}, verifyFunc(func(context.Context, string, Resolved) error { return removed }))
	if err != nil || !result.Applied || result.Verified || !errors.Is(result.Unverified, removed) {
		t.Fatalf("breaking upgrade = %+v, %v; want the pin applied with the failing code reported", result, err)
	}
	if want := []string{"go mod tidy", "go get github.com/sparkwing-dev/sparkwing@v0.20.0"}; strings.Join(ran, ";") != strings.Join(want, ";") {
		t.Fatalf("native commands = %q, want %q", ran, want)
	}
	pin, err := ReadPin(filepath.Join(repo, ".sparkwing"), engineid.GoSDK)
	if err != nil || pin.Version != "v0.20.0" {
		t.Fatalf("pin = %+v, %v; want v0.20.0", pin, err)
	}
	if guides := MigrationGuides("v0.10.2", "v0.20.0"); !strings.Contains(guides, "docs migrations --from v0.10.2 --to v0.20.0") || !strings.Contains(guides, "/tree/v0.20.0/docs/migrations") {
		t.Fatalf("migration guides = %q", guides)
	}
}

func TestOperationalVerificationFailureLeavesOriginalPins(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
	r.command = func(_ context.Context, stage, _ string, _ ...string) error {
		writeFixture(t, stage, "go.sum", []byte("updated checksums"))
		return nil
	}
	repo := t.TempDir()
	createUpgradeRepo(t, repo, engineid.GoSDK)
	download := errors.New(`upgrade: go mod download: exit status 1: dial tcp: lookup proxy.golang.org: no such host`)
	result, err := r.Upgrade(context.Background(), UpgradeRequest{Checkout: repo, SDK: engineid.GoSDK}, verifyFunc(func(context.Context, string, Resolved) error { return download }))
	if !errors.Is(err, download) || result.Applied {
		t.Fatalf("operational failure = %+v, %v; want an error and no applied pin", result, err)
	}
	pin, err := ReadPin(filepath.Join(repo, ".sparkwing"), engineid.GoSDK)
	if err != nil || pin.Version != "v0.10.2" {
		t.Fatalf("pin = %+v, %v; want the original v0.10.2", pin, err)
	}
	lock, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.sum"))
	if err != nil || string(lock) != "original native checksums\n" {
		t.Fatal("operational failure applied the lockfile")
	}
}

func TestConcurrentUpgradeLeavesOriginalPins(t *testing.T) {
	r, transport, key, _, _ := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
	r.command = func(_ context.Context, stage, _ string, _ ...string) error {
		writeFixture(t, stage, "go.sum", []byte("updated checksums"))
		return nil
	}
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprintf("verification_fails_%v", failing), func(t *testing.T) {
			repo := t.TempDir()
			createUpgradeRepo(t, repo, engineid.GoSDK)
			original, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			verifier := verifyFunc(func(context.Context, string, Resolved) error {
				writeFixture(t, filepath.Join(repo, ".sparkwing"), "go.mod", append(original, []byte("// concurrent user edit\n")...))
				if failing {
					return fmt.Errorf("%w: fixture type-check failed", ErrPipelineIncompatible)
				}
				return nil
			})
			result, err := r.Upgrade(context.Background(), UpgradeRequest{Checkout: repo, SDK: engineid.GoSDK}, verifier)
			if err == nil || result.Applied {
				t.Fatalf("concurrent upgrade applied: %+v %v", result, err)
			}
			requireCode(t, err, apierr.Conflict)
			current, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.mod"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(current), "v0.10.2") {
				t.Fatal("original pin changed")
			}
			lock, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.sum"))
			if err != nil || string(lock) != "original native checksums\n" {
				t.Fatal("failure partially applied lock")
			}
		})
	}
}

func TestBlockedRunnerCanFetchVerifiedReplacement(t *testing.T) {
	r, transport, key, policy, now := fixtureResolver(t)
	target := releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	policy.Minimum = Restriction{Version: "v0.20.0", Reason: "fixture security minimum", Security: true, AnnouncedAt: now, EffectiveAt: now}
	transport.policy(t, key, policy)
	transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
	update, err := r.PrepareRunnerUpdate(context.Background(), "v0.10.2", target)
	if err != nil {
		t.Fatal(err)
	}
	if !update.Required || update.Release.Version() != "v0.20.0" || update.Release.Target() != target {
		t.Fatalf("blocked update: %+v", update)
	}
	requests := len(transport.requests)
	if _, err := r.PrepareRunnerUpdate(context.Background(), "v0.10.2", target); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != requests {
		t.Fatal("runner cache missed")
	}
}

func TestUpgradeStagingDoesNotRecursivelyCopyItsOwnCache(t *testing.T) {
	source := t.TempDir()
	stage := filepath.Join(source, "engine-cache", "upgrade")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "fixture.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyCheckout(t.Context(), source, stage); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(stage, "fixture.txt"))
	if err != nil || string(body) != "source" {
		t.Fatalf("staged source = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "engine-cache", "upgrade")); !os.IsNotExist(err) {
		t.Fatalf("staging copied itself: %v", err)
	}
}

func TestUpgradeVerificationRetainsIsolatedGitMetadata(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%t", linked), func(t *testing.T) {
			git := func(dir string, args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			repo := t.TempDir()
			createUpgradeRepo(t, repo, engineid.GoSDK)
			git(repo, "init", "--initial-branch=main")
			git(repo, "add", ".")
			git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--no-verify", "-m", "fixture")
			if linked {
				worktree := filepath.Join(t.TempDir(), "linked")
				git(repo, "worktree", "add", "-b", "upgrade", worktree)
				repo = worktree
			}
			main := git(repo, "rev-parse", "HEAD")
			git(repo, "update-ref", "refs/remotes/origin/main", main)
			git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			git(repo, "checkout", "-b", "feature")
			git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--no-verify", "--allow-empty", "-m", "feature")
			head := git(repo, "rev-parse", "HEAD")
			git(repo, "update-ref", "refs/heads/main", head)
			writeFixture(t, repo, "unrelated.txt", []byte("uncommitted source"))
			git(repo, "add", "unrelated.txt")
			originalStatus := git(repo, "status", "--porcelain")
			r, transport, key, _, _ := fixtureResolver(t)
			target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
			transport.release(t, "v0.20.0", target, signedBundle(t, key, "v0.20.0", target, fixtureExecutable(t, "v0.20.0", target)))
			r.command = func(context.Context, string, string, ...string) error { return nil }
			result, err := r.Upgrade(t.Context(), UpgradeRequest{Checkout: repo, SDK: engineid.GoSDK}, verifyFunc(func(ctx context.Context, stage string, _ Resolved) error {
				if got := git(stage, "rev-parse", "HEAD"); got != head {
					t.Fatalf("verification HEAD = %s, want %s", got, head)
				}
				if got, want := git(stage, "for-each-ref", "--format=%(refname) %(objectname) %(symref)"), git(repo, "for-each-ref", "--format=%(refname) %(objectname) %(symref)"); got != want {
					t.Fatalf("verification refs = %s, want %s", got, want)
				}
				if got := git(stage, "rev-parse", "origin/main"); got != main {
					t.Fatalf("verification origin/main = %s, want %s", got, main)
				}
				if got := git(stage, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
					t.Fatalf("verification default branch = %s", got)
				}
				if got := git(stage, "rev-parse", "--show-toplevel"); got != stage {
					t.Fatalf("verification root = %s, want %s", got, stage)
				}
				body, err := os.ReadFile(filepath.Join(stage, "unrelated.txt"))
				if err != nil || string(body) != "uncommitted source" {
					t.Fatalf("staged source = %s, %v", body, err)
				}
				if !strings.Contains(git(stage, "status", "--porcelain"), ".sparkwing/go.mod") {
					t.Fatal("verification cannot see upgraded pin")
				}
				git(stage, "add", ".sparkwing/go.mod")
				if got := git(repo, "status", "--porcelain"); got != originalStatus {
					t.Fatalf("verification changed original index: %s", got)
				}
				return nil
			}))
			if err != nil || !result.Verified || !result.Applied {
				t.Fatalf("upgrade = %+v, %v", result, err)
			}
			if got := git(repo, "diff", "--cached", "--name-only"); got != "unrelated.txt" {
				t.Fatalf("original staged files = %s", got)
			}
		})
	}
}
