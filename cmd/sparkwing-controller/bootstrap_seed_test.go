package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
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
		for _, want := range []string{"bootstrap admin token", "random", "openssl rand -base64 32", "bootstrap-admin-token"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("startup refusal lacks %q: %v", want, err)
			}
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
