package orchestrator

import (
	"errors"
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func semaphoreClaims(names ...string) []wingwire.SemaphoreClaim {
	var claims []wingwire.SemaphoreClaim
	for _, name := range names {
		claims = append(claims, wingwire.SemaphoreClaim{Name: name, Capacity: 1, Cost: 1})
	}
	return claims
}

func TestAdmissionFailureReportsSlotFullOnlyForAClaimedGroup(t *testing.T) {
	err := admissionFailure(semaphoreClaims("deploy-lock"),
		&wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "deploy-lock"})
	if !strings.Contains(err.Error(), "slot full under OnLimit:Fail") {
		t.Fatalf("claimed group failure = %q, want the slot-full diagnosis", err)
	}
}

func TestAdmissionFailureNeverCallsAnUnclaimedKeyExhaustedCapacity(t *testing.T) {
	const storeSkew = "daemon v0.38.2 could not read the runs store: " +
		"sparkwing: database is at schema version 26; this binary expects 17"

	for _, tc := range []struct {
		name   string
		err    *wingdclient.AdmissionError
		expect []string
	}{
		{
			name:   "schema skew",
			err:    &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "terminal-check", Reason: storeSkew},
			expect: []string{storeSkew, "sparkwing daemon status"},
		},
		{
			name:   "terminal check without a reason",
			err:    &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "terminal-check"},
			expect: []string{`"terminal-check"`, "sparkwing daemon status"},
		},
		{
			name:   "cancelled run",
			err:    &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "cancelled"},
			expect: []string{`"cancelled"`},
		},
		{
			name:   "a key this client never claimed",
			err:    &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "some-future-daemon-key"},
			expect: []string{`"some-future-daemon-key"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admissionFailure(semaphoreClaims("deploy-lock"), tc.err)
			got := err.Error()
			if strings.Contains(got, "slot full") || strings.Contains(got, "sparkwing queue") {
				t.Fatalf("failure = %q, want no exhausted-capacity diagnosis", got)
			}
			var wrapped *wingdclient.AdmissionError
			if !errors.As(err, &wrapped) || wrapped != tc.err {
				t.Fatalf("failure %q lost the wrapped admission error", got)
			}
			for _, want := range tc.expect {
				if !strings.Contains(got, want) {
					t.Fatalf("failure = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestDaemonStoreSchemaSkewRefusesADaemonBehindTheStore(t *testing.T) {
	err := daemonStoreSchemaSkew("v0.38.2", "v0.39.0", 17, 26)
	if !errors.Is(err, ErrDaemonStoreSchemaTooOld) {
		t.Fatalf("skew error = %v, want ErrDaemonStoreSchemaTooOld", err)
	}
	for _, want := range []string{"v0.38.2", "v0.39.0", "17", "26", wingdclient.HostBinEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("skew error = %q, want it to name %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "respawns the same build") {
		t.Fatalf("skew error = %q, want it to say a daemon restart does not resolve this", err)
	}
}

func TestReasonlessDaemonKeysKeepTheirPreviousShape(t *testing.T) {
	for _, key := range []string{"duplicate", "parent", "reattach", "invalid"} {
		admErr := &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: key}
		got := admissionFailure(semaphoreClaims("deploy-lock"), admErr).Error()
		want := "local admission: " + admErr.Error()
		if got != want {
			t.Fatalf("%s failure = %q, want %q", key, got, want)
		}
	}
}

func TestDaemonStoreSchemaSkewAcceptsMatchingAndUnknownDaemons(t *testing.T) {
	for _, tc := range []struct {
		name         string
		daemonSchema int
	}{
		{"daemon predates the field", 0},
		{"daemon matches", 26},
		{"daemon is ahead", 27},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := daemonStoreSchemaSkew("v0.39.0", "v0.39.0", tc.daemonSchema, 26); err != nil {
				t.Fatalf("skew error = %v, want none", err)
			}
		})
	}
}

func TestNodeAdmissionFailureSeparatesTheClaimFromTheDaemonsOwnKeys(t *testing.T) {
	claim := wingwire.SemaphoreClaim{Name: "deploy-lock", Capacity: 1, Cost: 1}

	full := nodeAdmissionFailure(claim, &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "deploy-lock"})
	if full.Error() != `concurrency key "deploy-lock" slot full under OnLimit:Fail` {
		t.Fatalf("claimed key failure = %q", full)
	}

	const storeSkew = "daemon v0.38.2 could not read the runs store: database is at schema version 26; this binary expects 17"
	admErr := &wingdclient.AdmissionError{Policy: wingwire.PolicyFail, Key: "terminal-check", Reason: storeSkew}
	got := nodeAdmissionFailure(claim, admErr)
	if strings.Contains(got.Error(), "slot full") {
		t.Fatalf("node failure = %q, want no exhausted-capacity diagnosis", got)
	}
	for _, want := range []string{storeSkew, "sparkwing daemon status"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("node failure = %q, want it to contain %q", got, want)
		}
	}
	var wrapped *wingdclient.AdmissionError
	if !errors.As(got, &wrapped) || wrapped != admErr {
		t.Fatalf("node failure %q lost the wrapped admission error", got)
	}
}
