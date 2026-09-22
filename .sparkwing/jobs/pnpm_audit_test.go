package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pnpmCleanReport = `{"advisories":{},"metadata":{"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0},"totalDependencies":238}}`

func TestPnpmAuditSeparatesAnUnreachableRegistryFromAnAdvisory(t *testing.T) {
	timeout := `{"error":{"code":"ETIMEDOUT","summary":"request to https://registry.npmjs.org/-/npm/v1/security/advisories/bulk failed","detail":""}}`
	verdict, err := readPnpmAuditReport(timeout)
	if err != nil {
		t.Fatalf("decode a registry error: %v", err)
	}
	if verdict.Unavailable == nil {
		t.Fatal("a registry that could not answer read as an answer")
	}
	if len(verdict.Advisories) != 0 {
		t.Errorf("a registry error named advisories: %v", verdict.Advisories)
	}
	record, err := pnpmAuditOutcome(verdict)
	if record {
		t.Error("an unreachable registry recorded a pass; the next run would replay it")
	}
	if !errors.Is(err, errPnpmRegistryUnavailable) {
		t.Errorf("outcome error = %v, want one that identifies itself as unavailability", err)
	}

	advisory := `{"advisories":{"1088":{"module_name":"tar-fs","severity":"high"},"1065":{"module_name":"lodash","severity":"low"}},"metadata":{"vulnerabilities":{"low":1,"high":1,"critical":0},"totalDependencies":238}}`
	verdict, err = readPnpmAuditReport(advisory)
	if err != nil {
		t.Fatalf("decode an advisory report: %v", err)
	}
	if verdict.Unavailable != nil {
		t.Error("an advisory report read as unavailability")
	}
	if len(verdict.Advisories) != 1 || !strings.Contains(verdict.Advisories[0], "tar-fs") {
		t.Fatalf("advisories = %v, want the one high-severity package", verdict.Advisories)
	}
	record, err = pnpmAuditOutcome(verdict)
	if record {
		t.Error("an advisory recorded a pass; an unchanged lockfile would then skip the audit")
	}
	if err == nil || errors.Is(err, errPnpmRegistryUnavailable) {
		t.Errorf("outcome error = %v, want an advisory failure distinct from unavailability", err)
	}
}

func TestPnpmAuditFailsOnAnAdvisoryTheReportDoesNotName(t *testing.T) {
	// safety: npm counts vulnerabilities in metadata and names them per package;
	// a report that counts one and names none must still fail the gate.
	counted := `{"advisories":{},"metadata":{"vulnerabilities":{"high":0,"critical":2},"totalDependencies":238}}`
	verdict, err := readPnpmAuditReport(counted)
	if err != nil {
		t.Fatalf("decode a counted-only report: %v", err)
	}
	if len(verdict.Advisories) == 0 {
		t.Fatal("a report counting two critical advisories passed the gate")
	}
	if record, err := pnpmAuditOutcome(verdict); record || err == nil {
		t.Errorf("counted-only advisories gave record=%v err=%v", record, err)
	}
}

func TestPnpmAuditRecordsOnlyAClearRun(t *testing.T) {
	verdict, err := readPnpmAuditReport(pnpmCleanReport)
	if err != nil {
		t.Fatalf("decode a clean report: %v", err)
	}
	record, err := pnpmAuditOutcome(verdict)
	if !record || err != nil {
		t.Fatalf("a clean audit gave record=%v err=%v", record, err)
	}
}

func TestPnpmAuditRejectsAReportItCannotRead(t *testing.T) {
	for _, in := range []string{"", "   ", "npm ERR! code E401\n", "{"} {
		if _, err := readPnpmAuditReport(in); err == nil {
			t.Errorf("readPnpmAuditReport(%q) accepted output that is not a report", in)
		}
	}
	if _, err := readPnpmAuditReport("npm warn config production Use `--omit=dev`\n" + pnpmCleanReport); err != nil {
		t.Errorf("a report preceded by an npm warning was rejected: %v", err)
	}
}

func TestPnpmAuditRetriesOnlyWhatCanClearOnItsOwn(t *testing.T) {
	transient := []pnpmAuditError{
		{Code: "ETIMEDOUT"},
		{Code: "E502"},
		{Code: "ECONNRESET"},
		{Code: "EAI_AGAIN"},
		{Summary: "request failed, reason: socket hang up"},
		{Summary: "502 Bad Gateway"},
	}
	for _, e := range transient {
		if !pnpmErrorIsTransient(&e) {
			t.Errorf("%+v is not treated as transient, so the release still fails on a flaky registry", e)
		}
	}
	permanent := []pnpmAuditError{
		{Code: "E401", Summary: "Incorrect or missing password"},
		{Code: "EUSAGE", Summary: "This command requires an existing lockfile"},
	}
	for _, e := range permanent {
		if pnpmErrorIsTransient(&e) {
			t.Errorf("%+v is retried, which spends the whole budget on a failure that cannot clear", e)
		}
	}
}

func TestPnpmAuditProofExpiresInsideItsWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	digest := "d9c0ffee"

	if pnpmAuditProofFresh(dir, digest, now) {
		t.Fatal("an empty proof store reported a recorded pass")
	}
	if err := recordPnpmAuditProof(dir, digest, now); err != nil {
		t.Fatalf("record a pass: %v", err)
	}
	if !pnpmAuditProofFresh(dir, digest, now.Add(pnpmAuditRetention-time.Minute)) {
		t.Error("a pass inside the retention window was not reused")
	}
	if pnpmAuditProofFresh(dir, digest, now.Add(pnpmAuditRetention)) {
		t.Error("a pass at the retention boundary was reused; a new advisory against an unchanged lockfile would never be seen")
	}
	if pnpmAuditProofFresh(dir, "another-digest", now) {
		t.Error("a pass recorded for one dependency set was reused for another")
	}
	if pnpmAuditRetention > 24*time.Hour {
		t.Errorf("pnpm audit proofs are retained for %s; a security gate must re-ask the registry at least daily", pnpmAuditRetention)
	}
}

func TestPnpmAuditProofReuseIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	digest := "cafebabe"

	if err := os.WriteFile(filepath.Join(dir, digest), []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed an unreadable proof: %v", err)
	}
	if pnpmAuditProofFresh(dir, digest, now) {
		t.Error("an unparseable proof was reused")
	}

	body, err := json.Marshal(pnpmAuditProof{Format: pnpmAuditFormat + 1, Digest: digest, RecordedAt: now})
	if err != nil {
		t.Fatalf("encode a future-format proof: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, digest), body, 0o600); err != nil {
		t.Fatalf("seed a future-format proof: %v", err)
	}
	if pnpmAuditProofFresh(dir, digest, now) {
		t.Error("a proof written in another format was reused")
	}
}

func TestPnpmAuditDigestTracksTheDependencySet(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatalf("seed web dir: %v", err)
	}
	lock := filepath.Join(web, "pnpm-lock.yaml")
	manifest := filepath.Join(web, "package.json")
	if err := os.WriteFile(lock, []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
		t.Fatalf("seed lockfile: %v", err)
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"web"}`), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	first, err := pnpmAuditDigest(root)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	again, err := pnpmAuditDigest(root)
	if err != nil || again != first {
		t.Fatalf("digest is not stable: %q then %q (%v)", first, again, err)
	}

	if err := os.WriteFile(lock, []byte("lockfileVersion: '9.0'\nimporters:\n  .: {}\n"), 0o644); err != nil {
		t.Fatalf("change lockfile: %v", err)
	}
	changed, err := pnpmAuditDigest(root)
	if err != nil {
		t.Fatalf("digest after a lockfile change: %v", err)
	}
	if changed == first {
		t.Fatal("a changed lockfile kept its digest, so a new dependency set would reuse the old pass")
	}

	if err := os.Remove(lock); err != nil {
		t.Fatalf("remove lockfile: %v", err)
	}
	if _, err := pnpmAuditDigest(root); err == nil {
		t.Error("a missing lockfile produced a digest instead of refusing reuse")
	}
}

func TestPnpmAuditReadsTheFailurePayloadNpmActuallyWrites(t *testing.T) {
	// safety: captured from a real unreachable registry. The reason lands in the
	// top-level message, the error object is blank, and no report version is
	// present, so read as a report it names zero advisories and would pass.
	real := `{
  "message": "request to http://registry.invalid.test/-/npm/v1/security/audits/quick failed, reason: getaddrinfo ENOTFOUND registry.invalid.test",
  "error": {
    "summary": "",
    "detail": ""
  }
}`
	verdict, err := readPnpmAuditReport(real)
	if err != nil {
		t.Fatalf("decode npm's real failure payload: %v", err)
	}
	if verdict.Unavailable == nil {
		t.Fatal("npm's real failure payload read as a clean audit")
	}
	if !strings.Contains(verdict.Unavailable.Summary, "ENOTFOUND") {
		t.Errorf("the failure summary lost the reason: %q", verdict.Unavailable.Summary)
	}
	if !pnpmErrorIsTransient(verdict.Unavailable) {
		t.Error("an unresolvable registry is not retried")
	}
	if record, err := pnpmAuditOutcome(verdict); record || !errors.Is(err, errPnpmRegistryUnavailable) {
		t.Errorf("outcome for an unreachable registry = record %v, err %v", record, err)
	}
}

func TestPnpmAuditRefusesAPayloadThatIsNotAnAuditAnswer(t *testing.T) {
	if _, err := readPnpmAuditReport(`{"ok":true}`); err == nil {
		t.Error("a payload with no report version and no error passed as a clean audit")
	}
}

func stubPnpmAuditRunner(t *testing.T, answers ...func() (string, error)) *int {
	t.Helper()
	calls := 0
	prev := pnpmAuditRunner
	pnpmAuditRunner = func(context.Context) (string, error) {
		i := calls
		calls++
		if i >= len(answers) {
			i = len(answers) - 1
		}
		return answers[i]()
	}
	t.Cleanup(func() { pnpmAuditRunner = prev })
	return &calls
}

func unreachableRegistry() (string, error) {
	return `{"error":{"code":"ETIMEDOUT","summary":"request to https://registry.npmjs.org failed"}}`, nil
}

func cleanReport() (string, error) {
	return `{"advisories":{},"metadata":{"vulnerabilities":{"high":0,"critical":0},"totalDependencies":238}}`, nil
}

func TestRunPnpmAudit_RetriesATransientRegistryAndAcceptsALaterAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 9.0s of real work; the fast class runs under -short")
	}
	calls := stubPnpmAuditRunner(t, unreachableRegistry, unreachableRegistry, cleanReport)
	verdict, err := runPnpmAudit(t.Context())
	if err != nil {
		t.Fatalf("runPnpmAudit: %v", err)
	}
	if !verdict.Answered || len(verdict.Advisories) != 0 {
		t.Errorf("verdict = %+v, want an answered verdict naming no advisory", verdict)
	}
	if *calls != 3 {
		t.Errorf("ran npm %d time(s), want 3: two transient failures then an answer", *calls)
	}
}

func TestRunPnpmAudit_StopsAtTheAttemptCeilingAndReportsUnavailable(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 9.0s of real work; the fast class runs under -short")
	}
	calls := stubPnpmAuditRunner(t, unreachableRegistry)
	_, err := runPnpmAudit(t.Context())
	if !errors.Is(err, errPnpmRegistryUnavailable) {
		t.Fatalf("err = %v, want it to name the registry as unavailable rather than an advisory", err)
	}
	if *calls != pnpmAuditAttempts {
		t.Errorf("ran npm %d time(s), want the ceiling of %d", *calls, pnpmAuditAttempts)
	}
}

func TestRunPnpmAudit_DoesNotRetryAnAdvisory(t *testing.T) {
	advisory := func() (string, error) {
		return `{"advisories":{"1092":{"module_name":"left-pad","severity":"critical"}},"metadata":{"vulnerabilities":{"high":0,"critical":1},"totalDependencies":238}}`, nil
	}
	calls := stubPnpmAuditRunner(t, advisory)
	verdict, err := runPnpmAudit(t.Context())
	if err != nil {
		t.Fatalf("runPnpmAudit: %v", err)
	}
	if _, outcomeErr := pnpmAuditOutcome(verdict); outcomeErr == nil {
		t.Fatal("an advisory passed the gate")
	}
	if *calls != 1 {
		t.Errorf("ran npm %d time(s), want 1: an advisory is an answer, not a transient failure", *calls)
	}
}
