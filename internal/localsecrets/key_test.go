package localsecrets_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func keyFileIn(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "secrets.key")
	t.Setenv(localsecrets.KeyFileEnv, path)
	t.Setenv(localsecrets.KeyEnv, "")
	t.Setenv(localsecrets.PreviousKeyEnv, "")
	return path
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func loadRing(t *testing.T) *localsecrets.Keyring {
	t.Helper()
	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	return ring
}

func sealRow(t *testing.T, c controller.Cipher, st *store.Store, name, value string) {
	t.Helper()
	row := store.Secret{Name: name, Masked: true, Shared: true}
	sealed, err := controller.SealSecretValue(c, store.DefaultTeam, &row, value)
	if err != nil {
		t.Fatalf("seal %s: %v", name, err)
	}
	row.Value = sealed
	if err := st.CreateOrReplaceSecret(row, time.Now()); err != nil {
		t.Fatalf("store %s: %v", name, err)
	}
}

func openRow(t *testing.T, c controller.Cipher, st *store.Store, name string) (string, error) {
	t.Helper()
	sec, err := st.GetSecretRow(name, "")
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return controller.OpenSecretValue(c, store.DefaultTeam, sec)
}

func TestKeyring_FirstSealCreatesAPrivateKeyFile(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	st := openStore(t)
	ring := loadRing(t)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s before any seal = %v, want absent", path, err)
	}

	sealRow(t, ring.For(st), st, "TOKEN", "abc")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if info.Size() != secrets.KeySize {
		t.Errorf("key file holds %d bytes, want %d", info.Size(), secrets.KeySize)
	}
	if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
		t.Errorf("key file is not private: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "secrets.key" {
		t.Fatalf("temporary key artifacts remain: %v", entries)
	}
	sec, _ := st.GetSecretRow("TOKEN", "")
	if !secrets.IsBound(sec.Value) {
		t.Errorf("stored value %q is not a sealed envelope", sec.Value)
	}
	if got, err := openRow(t, loadRing(t).For(st), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("a second process opened %q, %v; want abc", got, err)
	}
}

func TestKeyring_ConcurrentCreatorsAgreeOnOneKey(t *testing.T) {
	keyFileIn(t, t.TempDir())
	st := openStore(t)
	const creators = 8
	rings := make([]*localsecrets.Keyring, creators)
	for i := range rings {
		rings[i] = loadRing(t)
	}
	envelopes := make([]string, creators)
	var wg sync.WaitGroup
	for i := range rings {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sealed, err := rings[i].For(st).Seal("same")
			if err != nil {
				t.Errorf("creator %d: %v", i, err)
			}
			envelopes[i] = sealed
		}(i)
	}
	wg.Wait()
	for i, env := range envelopes {
		for j, ring := range rings {
			if got, err := ring.For(st).Open(env); err != nil || got != "same" {
				t.Fatalf("creator %d opened creator %d's envelope as %q, %v; the creators hold different keys", j, i, got, err)
			}
		}
	}
	entries, err := os.ReadDir(filepath.Dir(os.Getenv(localsecrets.KeyFileEnv)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("key directory holds %d entries, want only secrets.key", len(entries))
	}
}

func TestKeyring_RefusesToCreateAKeyOverRowsSealedUnderAnother(t *testing.T) {
	st := openStore(t)
	keyFileIn(t, t.TempDir())
	sealRow(t, loadRing(t).For(st), st, "TOKEN", "abc")

	lost := keyFileIn(t, t.TempDir())
	ring := loadRing(t)
	_, err := ring.For(st).Seal("new")
	if !errors.Is(err, localsecrets.ErrNoKey) || !errors.Is(err, secrets.ErrKeyRefused) {
		t.Fatalf("seal with the key file gone = %v, want ErrNoKey marked as a key refusal", err)
	}
	for _, want := range []string{lost, localsecrets.KeyEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
	if _, statErr := os.Stat(lost); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a key was created at %s despite sealed rows", lost)
	}
	if err := ring.MissingKey(context.Background(), st); !errors.Is(err, localsecrets.ErrNoKey) {
		t.Errorf("MissingKey = %v, want ErrNoKey", err)
	}
}

func TestKeyring_EnvKeyOverridesTheKeyFile(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	envKey := bytes.Repeat([]byte{2}, secrets.KeySize)
	t.Setenv(localsecrets.KeyEnv, base64.StdEncoding.EncodeToString(envKey))
	st := openStore(t)

	ring := loadRing(t)
	sealRow(t, ring.For(st), st, "TOKEN", "abc")

	byEnv, err := secrets.NewCipher(envKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openRow(t, byEnv, st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("the env key opened %q, %v; want abc", got, err)
	}
	byFile, err := secrets.NewCipher(bytes.Repeat([]byte{1}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openRow(t, byFile, st, "TOKEN"); err == nil {
		t.Error("the key file opened a value sealed while the env key was set")
	}
}

func TestKeyring_PreviousKeyOpensWhatTheOldKeySealed(t *testing.T) {
	keyFileIn(t, t.TempDir())
	st := openStore(t)
	oldKey := bytes.Repeat([]byte{3}, secrets.KeySize)
	old, err := secrets.NewCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	sealRow(t, old, st, "TOKEN", "abc")

	t.Setenv(localsecrets.KeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, secrets.KeySize)))
	t.Setenv(localsecrets.PreviousKeyEnv, base64.StdEncoding.EncodeToString(oldKey))
	if got, err := openRow(t, loadRing(t).For(st), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("opened %q, %v under the previous key; want abc", got, err)
	}
}

func TestKeyring_OpenWithoutAKeySaysSo(t *testing.T) {
	keyFileIn(t, t.TempDir())
	if _, err := loadRing(t).For(nil).Open("enc:v1:AAAA"); !errors.Is(err, localsecrets.ErrNoKey) {
		t.Fatalf("open with no key = %v, want ErrNoKey", err)
	}
}

func TestKeyring_ReadsAKeyAnotherProcessCreatedLater(t *testing.T) {
	keyFileIn(t, t.TempDir())
	st := openStore(t)
	reader := loadRing(t)
	sealRow(t, loadRing(t).For(st), st, "TOKEN", "abc")
	if got, err := openRow(t, reader.For(st), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("a keyring loaded before the key existed opened %q, %v; want abc", got, err)
	}
}

func TestKeyring_RefusesAKeyFileOthersCanRead(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{5}, secrets.KeySize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{}); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("load with a group-readable key file = %v, want an owner-only refusal", err)
	}
}

func TestKeyring_RefusesAKeyFileSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.key")
	if err := os.WriteFile(target, bytes.Repeat([]byte{6}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	path := keyFileIn(t, dir)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("load through a symlinked key file = %v, want a symlink refusal", err)
	}
}

func TestKeyring_OnlyACreatingProcessMakesTheKey(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	st := openStore(t)
	ring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ring.For(st).Seal("value")
	if !errors.Is(err, localsecrets.ErrNoKeyToCreate) || !errors.Is(err, secrets.ErrKeyRefused) || !strings.Contains(err.Error(), localsecrets.KeyEnv) {
		t.Fatalf("seal in a non-creating process = %v, want ErrNoKeyToCreate naming %s", err, localsecrets.KeyEnv)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a non-creating process created %s", path)
	}

	sealRow(t, loadRing(t).For(st), st, "TOKEN", "abc")
	if got, err := openRow(t, ring.For(st), st, "TOKEN"); err != nil || got != "abc" {
		t.Fatalf("the non-creating process opened %q, %v once the key existed; want abc", got, err)
	}
}
