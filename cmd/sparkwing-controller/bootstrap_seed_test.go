package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestBootstrapStartupRefusesConstantSeedWithoutCreatingAdmin(t *testing.T) {
	st, err := openControllerStore(t.Context(), deployment.Config{}, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, value := range []byte{0, 255, 'A'} {
		raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
		err := ensureBootstrapAdmin(t.Context(), st, raw, logger)
		if err == nil {
			t.Errorf("constant seed %d accepted", value)
			continue
		}
		for _, want := range []string{"bootstrap admin token", "random", "Fresh store only", "openssl rand -base64 32", "bootstrap-admin-token"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("startup refusal lacks %q: %v", want, err)
			}
		}
		var output bytes.Buffer
		slog.New(controller.CredentialSafeLogHandler(slog.NewTextHandler(&output, nil))).Error("sparkwing-controller", "err", err)
		for _, want := range []string{"openssl rand -base64 32", "bootstrap-admin-token"} {
			if !strings.Contains(output.String(), want) || strings.Contains(output.String(), "[truncated]") {
				t.Errorf("logged startup refusal lost repair %q: %s", want, output.String())
			}
		}
		if strings.Contains(output.String(), raw) {
			t.Fatal("logged startup refusal exposes bootstrap token")
		}
		if strings.Contains(err.Error(), raw) {
			t.Fatal("startup refusal exposes bootstrap token")
		}
		if ready, err := st.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || ready {
			t.Fatalf("refused seed created administrator: %t %v", ready, err)
		}
	}
	var payload [32]byte
	if _, err := rand.Read(payload[:]); err != nil {
		t.Fatal(err)
	}
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(payload[:])
	if err := ensureBootstrapAdmin(t.Context(), st, raw, logger); err != nil {
		t.Fatalf("random bootstrap seed refused: %v", err)
	}
	if ready, err := st.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || !ready {
		t.Fatalf("random seed did not create administrator: %t %v", ready, err)
	}
}

func TestBootstrapStartupExistingWeakSeedRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := openControllerStore(t.Context(), deployment.Config{}, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var payload [32]byte
	if _, err := rand.Read(payload[:]); err != nil {
		t.Fatal(err)
	}
	normal := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(payload[:])
	if err := ensureBootstrapAdmin(t.Context(), st, normal, logger); err != nil {
		t.Fatal(err)
	}
	weak := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	// hack: bypass issuance to model a pre-existing weak credential.
	digest := sha256.Sum256([]byte(weak))
	db := teststore.SQLite(t, path)
	if _, err := db.ExecContext(t.Context(), `UPDATE capabilities SET digest=?`, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, seed string
		wants      []string
	}{
		{"same live seed", weak, []string{"live and guessable", "restart on loopback", "sparkwing operator tokens rotate", "--grace 0", "bootstrap-admin-token"}},
		{"unrelated weak seed", capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{255}, 32)), []string{"Existing store", "bootstrap-admin-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureBootstrapAdmin(t.Context(), st, tc.seed, logger)
			if err == nil {
				t.Fatal("weak seed allowed on existing store")
			}
			var output bytes.Buffer
			slog.New(controller.CredentialSafeLogHandler(slog.NewTextHandler(&output, nil))).Error("sparkwing-controller", "err", err)
			for _, want := range tc.wants {
				if !strings.Contains(output.String(), want) {
					t.Errorf("logged refusal lacks %q: %s", want, output.String())
				}
			}
			if strings.Contains(output.String(), "[truncated]") || strings.Contains(output.String(), tc.seed) || strings.Contains(output.String(), "openssl rand") {
				t.Fatalf("unsafe or misleading logged repair: %s", output.String())
			}
			if tc.name == "unrelated weak seed" && strings.Contains(output.String(), "live and guessable") {
				t.Fatal("unrelated seed classified as live")
			}
		})
	}
}
