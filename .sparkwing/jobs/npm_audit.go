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

const npmAuditCommand = "npm --prefix web audit --omit=dev --audit-level=high --json"

const (
	npmAuditAttempts       = 3
	npmAuditAttemptTimeout = 90 * time.Second
	npmAuditRetryBackoff   = 3 * time.Second
)

// safety: a recorded pass proves one lockfile against the advisory database of
// the moment it ran, so the window stays short enough that an advisory
// published against an unchanged lockfile still fails the next day's gate.
const npmAuditRetention = 24 * time.Hour

// safety: npmAuditFormat sits in the digest, so widening or narrowing the
// recorded inputs invalidates every recorded pass instead of reusing one that a
// different input set produced.
const npmAuditFormat = 1

// errNpmRegistryUnavailable marks a registry that could not answer, which is a
// different failure from an advisory it answered with. Both fail the gate.
var errNpmRegistryUnavailable = errors.New("advisory registry unavailable")

type npmAuditReport struct {
	// Version is npm's own marker that this payload is an audit answer. A
	// registry failure omits it, so requiring it keeps a failure payload from
	// reading as zero advisories.
	Version  int            `json:"auditReportVersion"`
	Message  string         `json:"message"`
	Error    *npmAuditError `json:"error"`
	Metadata struct {
		Vulnerabilities map[string]int `json:"vulnerabilities"`
	} `json:"metadata"`
	Vulnerabilities map[string]struct {
		Severity string `json:"severity"`
	} `json:"vulnerabilities"`
}

type npmAuditError struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

func (e *npmAuditError) String() string {
	if e.Code == "" {
		return e.Summary
	}
	return e.Code + ": " + e.Summary
}

// npmAuditVerdict is one audit run's answer: the advisories the registry
// reported, or the reason it reported none because it could not answer.
type npmAuditVerdict struct {
	Advisories  []string
	Unavailable *npmAuditError
}

var npmTransientCodes = map[string]bool{
	"EAI_AGAIN": true, "ECONNREFUSED": true, "ECONNRESET": true, "EHOSTUNREACH": true,
	"ENETUNREACH": true, "ENOTFOUND": true, "ETIMEDOUT": true,
	"E429": true, "E500": true, "E502": true, "E503": true, "E504": true,
}

var npmTransientMarkers = []string{
	"timeout", "timed out", "socket hang up", "bad gateway",
	"gateway time-out", "service unavailable", "network",
	"econnreset", "enotfound", "eai_again", "etimedout", "econnrefused",
	"502", "503", "504",
}

func npmErrorIsTransient(e *npmAuditError) bool {
	if e == nil {
		return false
	}
	if npmTransientCodes[strings.ToUpper(strings.TrimSpace(e.Code))] {
		return true
	}
	text := strings.ToLower(e.Summary + " " + e.Detail)
	for _, marker := range npmTransientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// readNpmAuditReport decodes one `npm audit --json` payload. npm reports a
// registry it could not reach in the same payload it reports advisories in, so
// the report -- not the exit code -- is what tells the two apart.
func readNpmAuditReport(stdout string) (npmAuditVerdict, error) {
	body := strings.TrimSpace(stdout)
	if start := strings.Index(body, "{"); start > 0 {
		body = body[start:]
	}
	if body == "" {
		return npmAuditVerdict{}, errors.New("npm audit produced no report")
	}
	var report npmAuditReport
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return npmAuditVerdict{}, fmt.Errorf("decode npm audit report: %w", err)
	}
	// npm puts the reason a failed audit failed in the top-level message and
	// leaves the error object blank, so the two are read together.
	if report.Error != nil || report.Message != "" {
		failure := npmAuditError{}
		if report.Error != nil {
			failure = *report.Error
		}
		if failure.Summary == "" {
			failure.Summary = report.Message
		}
		return npmAuditVerdict{Unavailable: &failure}, nil
	}
	if report.Version == 0 {
		return npmAuditVerdict{}, errors.New("npm audit produced no report version, so the payload is not an audit answer")
	}

	var advisories []string
	for name, v := range report.Vulnerabilities {
		if v.Severity == "high" || v.Severity == "critical" {
			advisories = append(advisories, name+" ("+v.Severity+")")
		}
	}
	sort.Strings(advisories)
	// safety: the per-package map and the metadata counters describe the same
	// audit, so trusting the higher count keeps a change in either shape from
	// reporting green.
	counted := report.Metadata.Vulnerabilities["high"] + report.Metadata.Vulnerabilities["critical"]
	if counted > len(advisories) {
		advisories = append(advisories, fmt.Sprintf("%d further high or critical advisory the report does not name", counted-len(advisories)))
	}
	return npmAuditVerdict{Advisories: advisories}, nil
}

// npmAuditOutcome turns a verdict into the gate's answer. A pass is recordable
// only when the registry answered and named nothing, so neither an advisory nor
// an unreachable registry can be replayed from the proof store.
func npmAuditOutcome(v npmAuditVerdict) (record bool, err error) {
	if v.Unavailable != nil {
		return false, fmt.Errorf("npm audit: %w (%s)", errNpmRegistryUnavailable, v.Unavailable)
	}
	if len(v.Advisories) > 0 {
		return false, fmt.Errorf("npm audit: %d high or critical advisory(ies) in production dependencies: %s",
			len(v.Advisories), strings.Join(v.Advisories, ", "))
	}
	return true, nil
}

func npmAuditOnce(ctx context.Context) (verdict npmAuditVerdict, retryable bool, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, npmAuditAttemptTimeout)
	defer cancel()

	res, runErr := sparkwing.Bash(attemptCtx, npmAuditCommand).Capture()
	stdout := res.Stdout
	var execErr *sparkwing.ExecError
	if errors.As(runErr, &execErr) {
		stdout = execErr.Stdout
	}

	verdict, parseErr := readNpmAuditReport(stdout)
	switch {
	case parseErr == nil && verdict.Unavailable != nil:
		return npmAuditVerdict{}, npmErrorIsTransient(verdict.Unavailable),
			fmt.Errorf("npm audit: %w (%s)", errNpmRegistryUnavailable, verdict.Unavailable)
	case parseErr == nil:
		return verdict, false, nil
	case ctx.Err() == nil && attemptCtx.Err() != nil:
		return npmAuditVerdict{}, true, fmt.Errorf("npm audit: %w (no answer within %s)", errNpmRegistryUnavailable, npmAuditAttemptTimeout)
	case runErr != nil:
		return npmAuditVerdict{}, false, fmt.Errorf("npm audit did not run: %w", runErr)
	}
	return npmAuditVerdict{}, false, fmt.Errorf("npm audit: %w", parseErr)
}

func runNpmAudit(ctx context.Context) (npmAuditVerdict, error) {
	var last error
	for attempt := 1; attempt <= npmAuditAttempts; attempt++ {
		verdict, retryable, err := npmAuditOnce(ctx)
		if err == nil {
			return verdict, nil
		}
		last = err
		if !retryable || attempt == npmAuditAttempts {
			break
		}
		sparkwing.Info(ctx, "npm audit: %v; retrying (attempt %d of %d)", err, attempt+1, npmAuditAttempts)
		if err := waitFor(ctx, npmAuditRetryBackoff*time.Duration(attempt)); err != nil {
			return npmAuditVerdict{}, err
		}
	}
	return npmAuditVerdict{}, last
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

// npmAuditDigest identifies the dependency set an audit answered about. The
// lockfile is what npm resolves advisories against; package.json rides along
// because it decides which of those entries `--omit=dev` drops.
func npmAuditDigest(root string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "format=%d\ncommand=%s\n", npmAuditFormat, npmAuditCommand)
	for _, name := range []string{"package-lock.json", "package.json"} {
		body, err := os.ReadFile(filepath.Join(root, "web", name))
		if err != nil {
			return "", fmt.Errorf("digest web/%s: %w", name, err)
		}
		fmt.Fprintf(h, "%s=%d\n", name, len(body))
		h.Write(body)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func npmAuditProofDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sparkwing", "npm-audit-proofs"), nil
}

type npmAuditProof struct {
	Format     int       `json:"format"`
	Digest     string    `json:"digest"`
	RecordedAt time.Time `json:"recorded_at"`
}

// npmAuditProofFresh reports whether this exact dependency set passed inside
// the retention window. Reuse is fail-closed: an unreadable, unparseable, or
// differently formatted record audits again.
func npmAuditProofFresh(dir, digest string, now time.Time) bool {
	if dir == "" || digest == "" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(dir, digest))
	if err != nil {
		return false
	}
	var proof npmAuditProof
	if err := json.Unmarshal(body, &proof); err != nil {
		return false
	}
	if proof.Format != npmAuditFormat || proof.Digest != digest {
		return false
	}
	age := now.Sub(proof.RecordedAt)
	return age >= 0 && age < npmAuditRetention
}

func recordNpmAuditProof(dir, digest string, now time.Time) error {
	if dir == "" || digest == "" {
		return errors.New("refusing to record an npm audit pass without a digest")
	}
	body, err := json.Marshal(npmAuditProof{Format: npmAuditFormat, Digest: digest, RecordedAt: now.UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	pruneNpmAuditProofs(dir, now)
	tmp, err := os.CreateTemp(dir, digest+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
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

func pruneNpmAuditProofs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= npmAuditRetention {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
