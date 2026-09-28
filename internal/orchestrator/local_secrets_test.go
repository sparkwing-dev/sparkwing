package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func testKeyring(t *testing.T) *localsecrets.Keyring {
	t.Helper()
	t.Setenv(localsecrets.KeyFileEnv, filepath.Join(t.TempDir(), "secrets.key"))
	t.Setenv(localsecrets.KeyEnv, "")
	t.Setenv(localsecrets.PreviousKeyEnv, "")
	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	return ring
}

func daemonSecretsClient(t *testing.T, sock string) *client.Client {
	t.Helper()
	httpClient := NewAPISocketClient(sock)
	t.Cleanup(httpClient.CloseIdleConnections)
	return client.New(HostedAPIBaseURL, httpClient)
}

func storedSecret(t *testing.T, home, name string) *store.Secret {
	t.Helper()
	st, err := store.OpenReadOnly(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer func() { _ = st.Close() }()
	sec, err := st.GetSecretRow(name, "")
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return sec
}

func TestDaemonAPI_SealsASecretARunThenReadsOverTheSocket(t *testing.T) {
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	ctx := context.Background()
	c := daemonSecretsClient(t, sock)

	health, err := ReadLocalAPIHealth(ctx, sock)
	if err != nil || health.Secrets != APISecretsSealed {
		t.Fatalf("health = %+v, %v; want secrets %q", health, err, APISecretsSealed)
	}
	if err := c.CreateSecretForPipeline(ctx, "TOKEN", "abc123", "", true, true); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if sec := storedSecret(t, home, "TOKEN"); !secrets.IsBound(sec.Value) || strings.Contains(sec.Value, "abc123") {
		t.Fatalf("state.db holds %q for TOKEN, want a sealed envelope", sec.Value)
	}

	seedRun(t, c, "run-1", "n")
	value, masked, err := localsecrets.SocketSource(ctx, c, "run-1").Read("TOKEN")
	if err != nil || value != "abc123" || !masked {
		t.Fatalf("run read TOKEN = %q, masked %v, %v; want abc123, masked", value, masked, err)
	}
	if _, _, err := localsecrets.SocketSource(ctx, c, "run-1").Read("ABSENT"); !errors.Is(err, secrets.ErrSecretMissing) {
		t.Fatalf("run read ABSENT = %v, want ErrSecretMissing", err)
	}
}

func TestDaemonAPI_SealsAPlaintextRowBeforeServing(t *testing.T) {
	home := wingdTestHome(t)
	seed, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.CreateOrReplaceSecret(store.Secret{Name: "OLD", Value: "plain-value", Masked: true, Shared: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startAPIDaemon(t, home, nil)
	sec, err := daemonSecretsClient(t, sock).GetSecret(context.Background(), "OLD")
	if err != nil || sec.Value != "plain-value" {
		t.Fatalf("GET OLD = %+v, %v; want plain-value", sec, err)
	}
	if stored := storedSecret(t, home, "OLD"); !secrets.IsBound(stored.Value) {
		t.Fatalf("state.db still holds %q for OLD, want it sealed", stored.Value)
	}
}

func TestDaemonAPI_RefusesSecretsWithoutTheKeyTheStoreNeeds(t *testing.T) {
	home := wingdTestHome(t)
	seed, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := secrets.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	row := store.Secret{Name: "SEALED", Masked: true, Shared: true}
	if row.Value, err = controller.SealSecretValue(other, store.DefaultTeam, &row, "x"); err != nil {
		t.Fatal(err)
	}
	if err := seed.CreateOrReplaceSecret(row, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startAPIDaemon(t, home, nil)
	c := daemonSecretsClient(t, sock)
	if err := c.CreateSecretForPipeline(context.Background(), "NEW", "y", "", true, true); err == nil {
		t.Fatal("the daemon stored a secret without the key its store is sealed under")
	}
	health, err := ReadLocalAPIHealth(context.Background(), sock)
	if err != nil || !strings.Contains(health.SecretsProblem, localsecrets.KeyEnv) {
		t.Fatalf("health = %+v, %v; want a secrets problem naming %s", health, err, localsecrets.KeyEnv)
	}
	if _, statErr := os.Stat(os.Getenv(localsecrets.KeyFileEnv)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the daemon created a second key: %v", statErr)
	}
	if _, err := c.ListRuns(context.Background(), store.RunFilter{}); err != nil {
		t.Fatalf("a missing secrets key stopped the runs routes: %v", err)
	}
}

func legacyConfigDir(t *testing.T, secretsEnv string) {
	t.Helper()
	if err := os.MkdirAll(paths.TestSandbox(), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg, err := os.MkdirTemp(paths.TestSandbox(), "xdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_HOME", "")
	if err := os.MkdirAll(filepath.Join(xdg, "sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "sparkwing", "secrets.env"), []byte(secretsEnv), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonAPI_ImportsLegacyFilesWhenItOpensTheStore(t *testing.T) {
	legacyConfigDir(t, "TOKEN=from-dotenv\n")
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	c := daemonSecretsClient(t, sock)
	seedRun(t, c, "run-1", "n")

	value, masked, err := localsecrets.SocketSource(context.Background(), c, "run-1").Read("TOKEN")
	if err != nil || value != "from-dotenv" || !masked {
		t.Fatalf("run read TOKEN = %q, masked %v, %v; want the imported value", value, masked, err)
	}
	if _, _, err := localsecrets.StoreSource(filepath.Join(home, "state.db"), "p").Read("TOKEN"); err != nil {
		t.Fatalf("a run reading the store after the import: %v", err)
	}
}

func TestDaemonAPI_AMalformedLegacyFileFailsSecretReadsWithTheImportError(t *testing.T) {
	legacyConfigDir(t, "TOKEN=fine\nthis is not dotenv\n")
	home := wingdTestHome(t)
	sock, _ := startAPIDaemon(t, home, nil)
	c := daemonSecretsClient(t, sock)
	seedRun(t, c, "run-1", "n")

	health, err := ReadLocalAPIHealth(context.Background(), sock)
	if err != nil || !strings.Contains(health.SecretsProblem, "secrets.env:2") {
		t.Fatalf("health = %+v, %v; want a secrets problem naming secrets.env:2", health, err)
	}
	for _, name := range []string{"TOKEN", "NEVER_SET"} {
		_, _, err := localsecrets.SocketSource(context.Background(), c, "run-1").Read(name)
		if err == nil || errors.Is(err, secrets.ErrSecretMissing) || !strings.Contains(err.Error(), "secrets.env:2") {
			t.Errorf("run read %s = %v, want the import error", name, err)
		}
	}
	_, _, err = localsecrets.StoreSource(filepath.Join(home, "state.db"), "p").Read("TOKEN")
	if err == nil || !strings.Contains(err.Error(), "have not been imported") {
		t.Errorf("a run reading the store directly = %v, want the pending import named", err)
	}
	if _, err := c.ListRuns(context.Background(), store.RunFilter{}); err != nil {
		t.Fatalf("a failed import stopped the runs routes: %v", err)
	}
}

func TestDaemonAPI_AWrongKeyImportsNothing(t *testing.T) {
	legacyConfigDir(t, "TOKEN=from-dotenv\n")
	home := wingdTestHome(t)
	seed, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := secrets.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	row := store.Secret{Name: "SEALED", Masked: true, Shared: true}
	if row.Value, err = controller.SealSecretValue(other, store.DefaultTeam, &row, "x"); err != nil {
		t.Fatal(err)
	}
	if err := seed.CreateOrReplaceSecret(row, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startAPIDaemonWith(t, home, nil, func(api *wingdAPI) {
		wrong := filepath.Join(t.TempDir(), "secrets.key")
		if err := os.WriteFile(wrong, bytes.Repeat([]byte{8}, secrets.KeySize), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(localsecrets.KeyFileEnv, wrong)
		ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
		if err != nil {
			t.Fatal(err)
		}
		api.secrets = ring
	})
	if health, err := ReadLocalAPIHealth(context.Background(), sock); err != nil || health.SecretsProblem == "" {
		t.Fatalf("health = %+v, %v; want a secrets problem from the wrong key", health, err)
	}
	st, err := store.OpenReadOnly(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.GetSecretRow("TOKEN", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TOKEN was imported under the wrong key: %v", err)
	}
}
