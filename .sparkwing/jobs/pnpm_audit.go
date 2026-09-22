package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const pnpmAuditCommand = "pnpm --dir web audit --prod --audit-level=high --json"

const (
	pnpmAuditAttempts       = 3
	pnpmAuditAttemptTimeout = 90 * time.Second
	pnpmAuditRetryBackoff   = 3 * time.Second
)

// safety: a recorded pass proves one lockfile against the advisory database of
// the moment it ran, so the window stays short enough that an advisory
// published against an unchanged lockfile still fails the next day's gate.
const pnpmAuditRetention = 24 * time.Hour

// safety: pnpmAuditFormat sits in the digest, so widening or narrowing the
// recorded inputs invalidates every recorded pass instead of reusing one that a
// different input set produced.
const pnpmAuditFormat = 2

// safety: a registry that could not answer is a different failure from an
// advisory it answered with. Both fail the gate.
var errPnpmRegistryUnavailable = errors.New("advisory registry unavailable")

// safety: pnpm answers in npm's legacy report shape, which carries no
// report-version marker. Its metadata block stands in as the proof that the
// payload is an audit answer, because a registry failure yields no metadata.
type pnpmAuditReport struct {
	Message  string          `json:"message"`
	Error    *pnpmAuditError `json:"error"`
	Metadata *struct {
		Vulnerabilities   map[string]int `json:"vulnerabilities"`
		TotalDependencies *int           `json:"totalDependencies"`
	} `json:"metadata"`
	Advisories map[string]struct {
		ModuleName string `json:"module_name"`
		Severity   string `json:"severity"`
	} `json:"advisories"`
}

type pnpmAuditError struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

func (e *pnpmAuditError) String() string {
	if e.Code == "" {
		return e.Summary
	}
	return e.Code + ": " + e.Summary
}

type pnpmAuditVerdict struct {
	// safety: the zero value must not read as a pass. Only a parsed audit
	// report sets Answered, so a verdict that never reached the registry -- a
	// zeroed struct from any early return -- fails the gate instead of
	// recording a proof.
	Answered    bool
	Advisories  []string
	Unavailable *pnpmAuditError
}

var pnpmTransientCodes = map[string]bool{
	"EAI_AGAIN": true, "ECONNREFUSED": true, "ECONNRESET": true, "EHOSTUNREACH": true,
	"ENETUNREACH": true, "ENOTFOUND": true, "ETIMEDOUT": true,
	"E429": true, "E500": true, "E502": true, "E503": true, "E504": true,
}

var pnpmTransientMarkers = []string{
	"timeout", "timed out", "socket hang up", "bad gateway",
	"gateway time-out", "service unavailable", "network",
	"econnreset", "enotfound", "eai_again", "etimedout", "econnrefused",
	"502", "503", "504",
}

func pnpmErrorIsTransient(e *pnpmAuditError) bool {
	if e == nil {
		return false
	}
	if pnpmTransientCodes[strings.ToUpper(strings.TrimSpace(e.Code))] {
		return true
	}
	text := strings.ToLower(e.Summary + " " + e.Detail)
	for _, marker := range pnpmTransientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// safety: npm reports an unreachable registry in the same payload it reports
// advisories in, so the report and not the exit code tells the two apart.
func readPnpmAuditReport(stdout string) (pnpmAuditVerdict, error) {
	body := strings.TrimSpace(stdout)
	if start := strings.Index(body, "{"); start > 0 {
		body = body[start:]
	}
	if body == "" {
		return pnpmAuditVerdict{}, errors.New("pnpm audit produced no report")
	}
	var report pnpmAuditReport
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return pnpmAuditVerdict{}, fmt.Errorf("decode pnpm audit report: %w", err)
	}
	// safety: npm puts the reason a failed audit failed in the top-level message
	// and leaves the error object blank, so the two are read together.
	if report.Error != nil || report.Message != "" {
		failure := pnpmAuditError{}
		if report.Error != nil {
			failure = *report.Error
		}
		if failure.Summary == "" {
			failure.Summary = report.Message
		}
		return pnpmAuditVerdict{Answered: true, Unavailable: &failure}, nil
	}
	if report.Metadata == nil || report.Metadata.TotalDependencies == nil {
		return pnpmAuditVerdict{}, errors.New("pnpm audit produced no report metadata, so the payload is not an audit answer")
	}

	var advisories []string
	for id, v := range report.Advisories {
		if v.Severity != "high" && v.Severity != "critical" {
			continue
		}
		name := v.ModuleName
		if name == "" {
			name = id
		}
		advisories = append(advisories, name+" ("+v.Severity+")")
	}
	sort.Strings(advisories)
	// safety: the per-package map and the metadata counters describe the same
	// audit, so trusting the higher count keeps a change in either shape from
	// reporting green.
	counted := report.Metadata.Vulnerabilities["high"] + report.Metadata.Vulnerabilities["critical"]
	if counted > len(advisories) {
		advisories = append(advisories, fmt.Sprintf("%d further high or critical advisory the report does not name", counted-len(advisories)))
	}
	return pnpmAuditVerdict{Answered: true, Advisories: advisories}, nil
}

// safety: a pass is recordable only where the registry answered and named
// nothing, so neither an advisory nor an unreachable registry is ever replayed
// out of the proof store.
func pnpmAuditOutcome(v pnpmAuditVerdict) (record bool, err error) {
	if !v.Answered {
		return false, fmt.Errorf("pnpm audit: %w (no audit report was read)", errPnpmRegistryUnavailable)
	}
	if v.Unavailable != nil {
		return false, fmt.Errorf("pnpm audit: %w (%s)", errPnpmRegistryUnavailable, v.Unavailable)
	}
	if len(v.Advisories) > 0 {
		return false, fmt.Errorf("pnpm audit: %d high or critical advisory(ies) in production dependencies: %s",
			len(v.Advisories), strings.Join(v.Advisories, ", "))
	}
	return true, nil
}

// safety: the retry policy is the deliverable here, and it cannot be exercised
// against a real registry, so the one process call sits behind a seam a test
// can replace.
var pnpmAuditRunner = func(ctx context.Context) (stdout string, err error) {
	res, runErr := sparkwing.Bash(ctx, pnpmAuditCommand).Capture()
	stdout = res.Stdout
	var execErr *sparkwing.ExecError
	if errors.As(runErr, &execErr) {
		stdout = execErr.Stdout
	}
	return stdout, runErr
}

func pnpmAuditOnce(ctx context.Context) (verdict pnpmAuditVerdict, retryable bool, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, pnpmAuditAttemptTimeout)
	defer cancel()

	stdout, runErr := pnpmAuditRunner(attemptCtx)

	verdict, parseErr := readPnpmAuditReport(stdout)
	switch {
	case parseErr == nil && verdict.Unavailable != nil:
		return pnpmAuditVerdict{}, pnpmErrorIsTransient(verdict.Unavailable),
			fmt.Errorf("pnpm audit: %w (%s)", errPnpmRegistryUnavailable, verdict.Unavailable)
	case parseErr == nil:
		return verdict, false, nil
	case ctx.Err() == nil && attemptCtx.Err() != nil:
		return pnpmAuditVerdict{}, true, fmt.Errorf("pnpm audit: %w (no answer within %s)", errPnpmRegistryUnavailable, pnpmAuditAttemptTimeout)
	case runErr != nil:
		return pnpmAuditVerdict{}, false, fmt.Errorf("pnpm audit did not run: %w", runErr)
	}
	return pnpmAuditVerdict{}, false, fmt.Errorf("pnpm audit: %w", parseErr)
}

func runPnpmAudit(ctx context.Context) (pnpmAuditVerdict, error) {
	var last error
	for attempt := 1; attempt <= pnpmAuditAttempts; attempt++ {
		verdict, retryable, err := pnpmAuditOnce(ctx)
		if err == nil {
			return verdict, nil
		}
		last = err
		if !retryable || attempt == pnpmAuditAttempts {
			break
		}
		sparkwing.Info(ctx, "pnpm audit: %v; retrying (attempt %d of %d)", err, attempt+1, pnpmAuditAttempts)
		if err := waitFor(ctx, pnpmAuditRetryBackoff*time.Duration(attempt)); err != nil {
			return pnpmAuditVerdict{}, err
		}
	}
	return pnpmAuditVerdict{}, last
}

func waitFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// safety: the lockfile is what npm resolves advisories against, and
// package.json decides which of those entries --prod drops, so both belong
// in the key. An .npmrc registry override and the npm version do not.
func pnpmAuditDigest(root string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "format=%d\ncommand=%s\n", pnpmAuditFormat, pnpmAuditCommand)
	for _, name := range []string{"pnpm-lock.yaml", "package.json"} {
		body, err := os.ReadFile(filepath.Join(root, "web", name))
		if err != nil {
			return "", fmt.Errorf("digest web/%s: %w", name, err)
		}
		fmt.Fprintf(h, "%s=%d\n", name, len(body))
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func pnpmAuditProofDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sparkwing", "pnpm-audit-proofs"), nil
}

type pnpmAuditProof struct {
	Format     int       `json:"format"`
	Digest     string    `json:"digest"`
	RecordedAt time.Time `json:"recorded_at"`
}

// safety: reuse is fail-closed. An unreadable, unparseable, or differently
// formatted record audits again rather than passing.
func pnpmAuditProofFresh(dir, digest string, now time.Time) bool {
	if dir == "" || digest == "" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, digest))
	if err != nil {
		return false
	}
	var proof pnpmAuditProof
	if err := json.Unmarshal(body, &proof); err != nil {
		return false
	}
	if proof.Format != pnpmAuditFormat || proof.Digest != digest {
		return false
	}
	age := now.Sub(proof.RecordedAt)
	return age >= 0 && age < pnpmAuditRetention
}

func recordPnpmAuditProof(dir, digest string, now time.Time) error {
	if dir == "" || digest == "" {
		return errors.New("refusing to record an pnpm audit pass without a digest")
	}
	body, err := json.Marshal(pnpmAuditProof{Format: pnpmAuditFormat, Digest: digest, RecordedAt: now.UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	prunePnpmAuditProofs(dir, now)
	tmp, err := os.CreateTemp(dir, digest+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { dropProofCleanupError(os.Remove(tmp.Name())) }()
	if _, err := tmp.Write(body); err != nil {
		dropProofCleanupError(tmp.Close())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, digest))
}

func prunePnpmAuditProofs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= pnpmAuditRetention {
			continue
		}
		dropProofCleanupError(os.Remove(filepath.Join(dir, e.Name())))
	}
}

// safety: a leftover proof cannot admit a stale pass, because reuse is keyed by
// digest and bounded by pnpmAuditRetention. Naming the failure is worth it;
// failing a security gate over it is not.
func dropProofCleanupError(err error) {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return
	}
	fmt.Fprintf(os.Stderr, "pnpm audit: could not remove a proof file: %v\n", err)
}
