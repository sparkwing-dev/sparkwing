package jobs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

func completeProofEnv() proofEnv {
	return proofEnv{Sparkwing: "tree-a", SparksCore: "core-a", Toolchain: "go-a", Reusable: true}
}

func verifyManifest(t *testing.T, name string) templates.Manifest {
	t.Helper()
	tmpl, err := templates.Get(name)
	if err != nil {
		t.Fatalf("load template %s: %v", name, err)
	}
	return tmpl.Manifest
}

func mustDigest(t *testing.T, env proofEnv, m templates.Manifest) string {
	t.Helper()
	d, err := templateProofDigest(context.Background(), env, m)
	if err != nil {
		t.Fatalf("digest %s: %v", m.Name, err)
	}
	return d
}

func TestTemplateProofDigestRefusesIncompleteInputs(t *testing.T) {
	m := verifyManifest(t, "lint-test-go")
	_, err := templateProofDigest(context.Background(), proofEnv{Reason: "no local sparks-core checkout"}, m)
	if err == nil || !strings.Contains(err.Error(), "no local sparks-core checkout") {
		t.Fatalf("digest on incomplete inputs = %v, want a refusal naming the missing input", err)
	}

	digest, dir, reuse := reusableProof(context.Background(), proofEnv{Reason: "exhaustive proof requested"}, m)
	if reuse || digest != "" || dir != "" {
		t.Fatalf("reusableProof = (%q, %q, %v), want no reuse and no digest", digest, dir, reuse)
	}
}

func TestTemplateProofDigestCoversEverySharedInput(t *testing.T) {
	m := verifyManifest(t, "lint-test-go")
	base := mustDigest(t, completeProofEnv(), m)

	for _, tc := range []struct {
		name string
		env  proofEnv
	}{
		{"sparkwing checkout", proofEnv{Sparkwing: "tree-b", SparksCore: "core-a", Toolchain: "go-a", Reusable: true}},
		{"sparks-core checkout", proofEnv{Sparkwing: "tree-a", SparksCore: "core-b", Toolchain: "go-a", Reusable: true}},
		{"toolchain", proofEnv{Sparkwing: "tree-a", SparksCore: "core-a", Toolchain: "go-b", Reusable: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustDigest(t, tc.env, m); got == base {
				t.Fatalf("a changed %s left the digest at %s", tc.name, base)
			}
		})
	}

	if mustDigest(t, completeProofEnv(), m) != base {
		t.Fatal("the digest is not stable across calls with identical inputs")
	}
	if other := mustDigest(t, completeProofEnv(), verifyManifest(t, "lint-test-node")); other == base {
		t.Fatal("two templates share a digest")
	}
}

func TestManifestVerifyDigestTracksVerificationFieldsOnly(t *testing.T) {
	m := verifyManifest(t, "lint-test-go")
	base, err := manifestVerifyDigest(m)
	if err != nil {
		t.Fatal(err)
	}

	prose := m
	prose.Description = "a different description"
	prose.WhenToUse = "a different signal"
	if got, err := manifestVerifyDigest(prose); err != nil || got != base {
		t.Fatalf("prose-only manifest change moved the digest (%v)", err)
	}

	for _, mutate := range []func(templates.Manifest) templates.Manifest{
		func(x templates.Manifest) templates.Manifest { x.Verify = templates.VerifyCompileOnly; return x },
		func(x templates.Manifest) templates.Manifest { x.VerifyFixture = templates.FixtureDocker; return x },
		func(x templates.Manifest) templates.Manifest { x.VerifyTools = []string{"migrate"}; return x },
		func(x templates.Manifest) templates.Manifest {
			params := map[string]string{"changed": "value"}
			for k, v := range x.VerifyParams {
				params[k] = v
			}
			x.VerifyParams = params
			return x
		},
	} {
		got, err := manifestVerifyDigest(mutate(m))
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatal("a verification-field change left the manifest digest unchanged")
		}
	}
}

func TestTemplateContentDigestSeparatesTemplates(t *testing.T) {
	first, err := templateContentDigest("lint-test-go")
	if err != nil {
		t.Fatal(err)
	}
	again, err := templateContentDigest("lint-test-go")
	if err != nil || again != first {
		t.Fatalf("content digest is not stable: %q vs %q (%v)", first, again, err)
	}
	other, err := templateContentDigest("lint-test-node")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two templates share a content digest")
	}
	if _, err := templateContentDigest("no-such-template"); err == nil {
		t.Fatal("an unknown template produced a digest")
	}
}

func seedGitCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	gitInTest(t, dir, "init", "--quiet")
	commit(t, dir, "seed")
	return dir
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	ident := []string{"-c", "user.email=t@example.invalid", "-c", "user.name=t"}
	gitInTest(t, dir, append(ident, "add", "-A")...)
	gitInTest(t, dir, append(ident, "commit", "--quiet", "-m", message)...)
}

func gitInTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutDigestTracksTrackedAndUntrackedChanges(t *testing.T) {
	ctx := context.Background()
	dir := seedGitCheckout(t)

	clean, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatalf("digest clean checkout: %v", err)
	}

	write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	dirty, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatalf("digest dirty checkout: %v", err)
	}
	if dirty == clean {
		t.Fatal("an edited tracked file left the checkout digest unchanged")
	}

	write(t, filepath.Join(dir, "extra.go"), "package main\n")
	withUntracked, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatalf("digest checkout with an untracked file: %v", err)
	}
	if withUntracked == dirty {
		t.Fatal("a new untracked file left the checkout digest unchanged")
	}

	if _, err := checkoutDigest(ctx, t.TempDir()); err == nil {
		t.Fatal("a directory that is not a checkout produced a digest")
	}
	if _, err := checkoutDigest(ctx, ""); err == nil {
		t.Fatal("an empty directory produced a digest")
	}
}

func TestResolveProofEnvRefusesWhatItCannotDigest(t *testing.T) {
	ctx := context.Background()
	core := map[string]string{"github.com/sparkwing-dev/sparks-core/templates": t.TempDir()}

	if env := resolveProofEnv(ctx, seedGitCheckout(t), core, true); env.Reusable || env.Reason != "exhaustive proof requested" {
		t.Fatalf("exhaustive env = %+v, want no reuse", env)
	}
	if env := resolveProofEnv(ctx, seedGitCheckout(t), nil, false); env.Reusable || !strings.Contains(env.Reason, "sparks-core") {
		t.Fatalf("env without a local sparks-core = %+v, want a refusal naming it", env)
	}
	if env := resolveProofEnv(ctx, t.TempDir(), core, false); env.Reusable || !strings.Contains(env.Reason, "sparkwing checkout") {
		t.Fatalf("env over a non-checkout = %+v, want a refusal naming it", env)
	}
}

func TestProofStoreOnlyMatchesARecordedDigest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proofs")
	if proofRecorded(dir, "abc123") {
		t.Fatal("an empty store matched a digest")
	}
	if err := recordProof(dir, "abc123", proofRecord{Template: "lint-test-go", Tier: templates.VerifyRunnable, RanRunStep: true}); err != nil {
		t.Fatalf("record proof: %v", err)
	}
	if !proofRecorded(dir, "abc123") {
		t.Fatal("a recorded digest did not match")
	}
	if proofRecorded(dir, "abc124") {
		t.Fatal("a near-miss digest matched")
	}

	body, err := os.ReadFile(filepath.Join(dir, "abc123"))
	if err != nil {
		t.Fatal(err)
	}
	var rec proofRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("proof body is not JSON: %v", err)
	}
	if rec.Format != proofFormat || rec.Template != "lint-test-go" || rec.Tier != templates.VerifyRunnable || !rec.RanRunStep || rec.RecordedAt.IsZero() {
		t.Fatalf("proof body = %+v", rec)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("proof directory holds %d entries, want only the finished proof", len(entries))
	}
}

func TestProofStoreRejectsUnusableRecords(t *testing.T) {
	dir := t.TempDir()
	if proofRecorded(dir, "") {
		t.Fatal("an empty digest matched a proof")
	}
	if proofRecorded("", "abc123") {
		t.Fatal("an empty directory matched a proof")
	}
	if err := recordProof(dir, "", proofRecord{Template: "lint-test-go"}); err == nil {
		t.Fatal("recording without a digest succeeded")
	}

	write(t, filepath.Join(dir, "notjson"), "lint-test-go\n")
	if proofRecorded(dir, "notjson") {
		t.Fatal("a proof body that does not parse matched")
	}
	stale, err := json.Marshal(proofRecord{Format: proofFormat + 1, Template: "lint-test-go"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "oldformat"), string(stale))
	if proofRecorded(dir, "oldformat") {
		t.Fatal("a proof from another input set matched")
	}
}

func TestRecordProofPrunesExpiredProofs(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "expired")
	write(t, old, "{}")
	stale := time.Now().Add(-proofRetention - time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "recent")
	write(t, fresh, "{}")

	if err := recordProof(dir, "abc123", proofRecord{Template: "lint-test-go"}); err != nil {
		t.Fatalf("record proof: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an expired proof survived recording: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a fresh proof was pruned: %v", err)
	}
}

func TestToolReachabilityDistinguishesTheDockerDaemon(t *testing.T) {
	ctx := context.Background()
	other := toolReachability(ctx, "node", "/usr/bin/node:v22")
	if other != "/usr/bin/node:v22" {
		t.Fatalf("non-docker identity = %q, want it untouched", other)
	}
	got := toolReachability(ctx, "docker", "/usr/bin/docker:27")
	if !strings.HasPrefix(got, "/usr/bin/docker:27:daemon=") {
		t.Fatalf("docker identity = %q, want the daemon probe appended", got)
	}
	if !strings.HasSuffix(got, "true") && !strings.HasSuffix(got, "false") {
		t.Fatalf("docker identity = %q, want a boolean reachability", got)
	}
}

func TestCheckoutDigestCoversGitignoredBuildInputs(t *testing.T) {
	ctx := context.Background()
	dir := seedGitCheckout(t)
	write(t, filepath.Join(dir, ".gitignore"), "go.work\ninternal/web/next-out/\n")
	commit(t, dir, "ignore build inputs")

	base, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse .\n")
	withWork, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if withWork == base {
		t.Fatal("an ignored go.work left the checkout digest unchanged")
	}

	bundle := filepath.Join(dir, "internal", "web", "next-out")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(bundle, "index.html"), "<title>a</title>")
	withBundle, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if withBundle == withWork {
		t.Fatal("an ignored embedded web bundle left the checkout digest unchanged")
	}

	write(t, filepath.Join(bundle, "index.html"), "<title>b</title>")
	changed, err := checkoutDigest(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == withBundle {
		t.Fatal("editing the embedded web bundle left the checkout digest unchanged")
	}
}

func TestSkippedRunStepIsNeverRecordedAsAProof(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	recordVerification(ctx, dir, "skipped-digest", "db-migrate-updown",
		templateOutcome{Tier: templates.VerifyRunnable, Skipped: true, SkipReason: "docker daemon"})
	if proofRecorded(dir, "skipped-digest") {
		t.Fatal("a template whose run step was skipped recorded a proof")
	}

	recordVerification(ctx, dir, "ran-digest", "db-migrate-updown",
		templateOutcome{Tier: templates.VerifyRunnable, RanRunStep: true})
	if !proofRecorded(dir, "ran-digest") {
		t.Fatal("a template that ran did not record a proof")
	}

	recordVerification(ctx, dir, "compile-digest", "lint-test-go",
		templateOutcome{Tier: templates.VerifyCompileOnly})
	if !proofRecorded(dir, "compile-digest") {
		t.Fatal("a compile-only template, which has no run step to skip, did not record a proof")
	}

	recordVerification(ctx, dir, "", "lint-test-go", templateOutcome{Tier: templates.VerifyCompileOnly})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("proof directory holds %d entries, want the two recordable proofs", len(entries))
	}
}
