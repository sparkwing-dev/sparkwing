//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func TestRunnersDeleteServiceUserKeepsTheRecordUntilTheRevokeSucceeds(t *testing.T) {
	f := newRunnersFixture(t)
	p := &fakePrivilege{reply: installedReply("sw-ci")}
	swapServiceUserPrivilege(t, p)
	captureStdout(t, func() {
		if err := f.run([]string{"add", "--profile", "prod", "--runner", "desk", "--repos", "github.com/acme/api", "--service-user", "sw-ci"}); err != nil {
			t.Fatal(err)
		}
	})
	newInstallFixture(t)
	record := enrollmentRecordPath("linux", "sw-ci")
	if err := writeEnrollmentRecord(record, serviceUserRemoved{Runner: "desk", Controller: "https://controller.example", TokenPrefix: f.tokens[0].Prefix}); err != nil {
		t.Fatal(err)
	}
	// safety: a job deleted the account's config, so the record is the only name of the token.
	serviceUserRunAs = func(context.Context, runnerservice.Account, []string, []byte) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	p.reply = func(args []string, _ []byte) ([]byte, error) {
		if slices.Contains(args, "--revoked") {
			path, err := forgetServiceUserEnrollment("linux", "sw-ci")
			if err != nil {
				return nil, err
			}
			return json.Marshal(struct{ Record string }{path})
		}
		removed, err := uninstallServiceUser(t.Context(), "linux", "sw-ci")
		if err != nil {
			return nil, err
		}
		return json.Marshal(removed)
	}
	f.revokeErr = errors.New("controller unavailable")
	err := f.run([]string{"delete", "--profile", "prod", "--service-user", "sw-ci"})
	if !errors.Is(err, f.revokeErr) || !strings.Contains(err.Error(), f.tokens[0].Prefix) || !strings.Contains(err.Error(), "rerun sparkwing runners delete --profile prod --service-user sw-ci") {
		t.Fatalf("failed revoke = %v, want the token prefix and the retry", err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("a failed revoke lost the enrollment record: %v", err)
	}
	f.revokeErr = nil
	captureStdout(t, func() {
		if err := f.run([]string{"delete", "--profile", "prod", "--service-user", "sw-ci"}); err != nil {
			t.Fatalf("retry after a failed revoke: %v", err)
		}
	})
	if f.tokens[0].RevokedAt == nil {
		t.Fatal("the retry left the runner token live")
	}
	if _, err := os.Lstat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the enrollment record outlived the revoked token: %v", err)
	}
}
