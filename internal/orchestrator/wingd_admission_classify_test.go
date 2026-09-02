package orchestrator

import (
	"errors"
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func semaphoreRequest(names ...string) wingwire.AdmissionRequest {
	req := wingwire.AdmissionRequest{RunID: "r1"}
	for _, name := range names {
		req.Semaphores = append(req.Semaphores, wingwire.SemaphoreClaim{Name: name, Capacity: 1, Cost: 1})
	}
	return req
}

func TestAdmissionFailureReportsSlotFullOnlyForAClaimedGroup(t *testing.T) {
	err := admissionFailure(semaphoreRequest("deploy-lock"),
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
			expect: []string{"the daemon gave no reason", "sparkwing daemon status"},
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
			got := admissionFailure(semaphoreRequest("deploy-lock"), tc.err).Error()
			if strings.Contains(got, "slot full") || strings.Contains(got, "sparkwing queue") {
				t.Fatalf("failure = %q, want no exhausted-capacity diagnosis", got)
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
	for _, want := range []string{"v0.38.2", "v0.39.0", "17", "26", "sparkwing daemon restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("skew error = %q, want it to name %q", err, want)
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
