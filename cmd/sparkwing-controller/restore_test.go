package main

import (
	"bytes"
	"errors"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRestoreHelpExplainsSecuritySignOut(t *testing.T) {
	var out, stderr bytes.Buffer
	if err := runRestore([]string{"prepare", "--help"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--sign-out-everyone", "security incidents", "stolen database", "leaked keys"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("restore help lacks %q: %s", text, &stderr)
		}
	}
	if err := runRestore([]string{"acknowledge", "--sign-out-everyone"}, &out, &stderr); err == nil {
		t.Fatal("acknowledgment accepted a preparation-only option")
	}
}

func TestRestoreCommandPreservesCredentialsUnlessSignOutRequested(t *testing.T) {
	for _, signOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "sign-out-everyone"}[signOut], func(t *testing.T) {
			checkRestoreCommand(t, signOut)
		})
	}
}

func checkRestoreCommand(t *testing.T, signOut bool) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	var out, stderr bytes.Buffer
	invoke := func(verb string) error {
		out.Reset()
		args := []string{verb}
		if verb == "prepare" && signOut {
			args = append(args, "--sign-out-everyone")
		}
		return runRestore(args, &out, &stderr)
	}
	if err := invoke("prepare"); err == nil {
		t.Fatal("restore created an empty database")
	}
	path := filepath.Join(home, "state.db")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed restore created state: %v", err)
	}
	before, err := credentials.LoadOrCreateEpoch(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path, store.WithCredentialEpoch(before))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	checkToken := func(st *store.Store, status int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/tokens", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		controller.New(st, nil).Handler().ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("token HTTP status = %d, want %d: %s", rec.Code, status, rec.Body)
		}
	}
	checkToken(st, http.StatusOK)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invoke("prepare"); err != nil {
		t.Fatal(err)
	}
	rotated, err := credentials.LoadOrCreateEpoch(home)
	if err != nil || (rotated != before) != signOut {
		t.Fatalf("epoch changed=%t, sign-out=%t: %v", rotated != before, signOut, err)
	}
	if err := invoke("prepare"); err != nil {
		t.Fatal(err)
	}
	again, err := credentials.LoadOrCreateEpoch(home)
	if err != nil || (again != rotated) != signOut {
		t.Fatalf("repeated preparation epoch changed=%t, sign-out=%t: %v", again != rotated, signOut, err)
	}
	st, err = store.Open(path, store.WithCredentialEpoch(again))
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusOK
	if signOut {
		status = http.StatusUnauthorized
	}
	checkToken(st, status)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invoke("reconcile"); err == nil {
		t.Fatal("removed reconcile command was accepted")
	}
	if err := invoke("acknowledge"); err != nil {
		t.Fatal(err)
	}
	var state store.RestoreState
	if err := json.Unmarshal(out.Bytes(), &state); err != nil || state.Pending || state.AcknowledgedAt == 0 {
		t.Fatalf("finished restore = %+v, %v", state, err)
	}
}

func TestRestoreUsesSeparatePersistentEpochDirectory(t *testing.T) {
	home, creds, epochDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := credentials.LoadOrCreateEpoch(epochDir)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := runRestore([]string{"prepare", "--sign-out-everyone", "--credentials-dir", creds, "--credential-epoch-dir", epochDir}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	after, err := credentials.LoadOrCreateEpoch(epochDir)
	if err != nil || after == before {
		t.Fatalf("separate epoch did not rotate: %v", err)
	}
	st, err = store.Open(filepath.Join(home, "state.db"), store.WithCredentialEpoch(after))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDeploymentRecord(t.Context(), filepath.Join(home, "state.db"), deployment.Config{}, creds, epochDir, st); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	adminStore, err := openDeclaredStore(t.Context(), filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := adminStore.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, creds} {
		if _, err := os.Stat(filepath.Join(dir, "credential-epoch")); !os.IsNotExist(err) {
			t.Fatalf("epoch appeared outside its persistent directory %s: %v", dir, err)
		}
	}
}

func TestRestorePrepareRefusesALostEpochFileAndNamesTheWayOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	epoch, err := credentials.LoadOrCreateEpoch(home)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "state.db")
	st, err := store.Open(path, store.WithCredentialEpoch(epoch))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureCredentialEpoch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "credential-epoch")); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err = runRestore([]string{"prepare"}, &out, &stderr)
	if !errors.Is(err, store.ErrCredentialEpochChanged) || !strings.Contains(err.Error(), filepath.Join(home, "credential-epoch")) || !strings.Contains(err.Error(), "--sign-out-everyone") {
		t.Fatalf("lost epoch file was not refused with its remedy: %v", err)
	}
	st, err = store.Open(path, store.WithCredentialEpoch(epoch))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if state, err := st.AsOperator().RestoreStatus(t.Context()); err != nil || state.Pending {
		t.Fatalf("refused restore held schedules: %+v %v", state, err)
	}
}
