package localsecrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testhome"
)

func keyFileIn(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "secrets.key")
	t.Setenv(KeyFileEnv, path)
	t.Setenv(keyEnv, "")
	t.Setenv(previousKeyEnv, "")
	return path
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := teststore.OpenExisting(t, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func loadRing(t *testing.T) *Keyring {
	t.Helper()
	ring, err := LoadKeyring(KeyringOptions{Create: true})
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
	if err := teststore.SeedSecret(st, store.DefaultTeam, row, time.Now()); err != nil {
		t.Fatalf("store %s: %v", name, err)
	}
}

func openRow(t *testing.T, c controller.Cipher, st *store.Store, name string) (string, error) {
	t.Helper()
	sec, err := teststore.SecretRow(teststore.ForTeam(t, st, store.DefaultTeam), name, "", "")
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

	sealRow(t, ring.For(st.AsOperator()), st, "TOKEN", "abc")

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
	sec, _ := teststore.SecretRow(teststore.ForTeam(t, st, store.DefaultTeam), "TOKEN", "", "")
	if !secrets.IsBound(sec.Value) {
		t.Errorf("stored value %q is not a sealed envelope", sec.Value)
	}
	if got, err := openRow(t, loadRing(t).For(st.AsOperator()), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("a second process opened %q, %v; want abc", got, err)
	}
}

func TestKeyring_ConcurrentCreatorsAgreeOnOneKey(t *testing.T) {
	keyFileIn(t, t.TempDir())
	st := openStore(t)
	const creators = 8
	rings := make([]*Keyring, creators)
	for i := range rings {
		rings[i] = loadRing(t)
	}
	envelopes := make([]string, creators)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range rings {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sealed, err := rings[i].For(st.AsOperator()).Seal("same")
			if err != nil {
				t.Errorf("creator %d: %v", i, err)
			}
			envelopes[i] = sealed
		}(i)
	}
	close(start)
	wg.Wait()
	for i, env := range envelopes {
		for j, ring := range rings {
			if got, err := ring.For(st.AsOperator()).Open(env); err != nil || got != "same" {
				t.Fatalf("creator %d opened creator %d's envelope as %q, %v; the creators hold different keys", j, i, got, err)
			}
		}
	}
	entries, err := os.ReadDir(filepath.Dir(os.Getenv(KeyFileEnv)))
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
	sealRow(t, loadRing(t).For(st.AsOperator()), st, "TOKEN", "abc")

	lost := keyFileIn(t, t.TempDir())
	ring := loadRing(t)
	_, err := ring.For(st.AsOperator()).Seal("new")
	if !errors.Is(err, errNoKey) || !errors.Is(err, secrets.ErrKeyRefused) {
		t.Fatalf("seal with the key file gone = %v, want ErrNoKey marked as a key refusal", err)
	}
	for _, want := range []string{lost, keyEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
	if _, statErr := os.Stat(lost); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a key was created at %s despite sealed rows", lost)
	}
	if err := ring.MissingKey(context.Background(), st.AsOperator()); !errors.Is(err, errNoKey) {
		t.Errorf("MissingKey = %v, want ErrNoKey", err)
	}
}

func TestKeyring_EnvKeyOverridesTheKeyFile(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	envKey := bytes.Repeat([]byte{2}, secrets.KeySize)
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(envKey))
	st := openStore(t)

	ring := loadRing(t)
	sealRow(t, ring.For(st.AsOperator()), st, "TOKEN", "abc")

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

	bound, err := old.SealBound(secrets.PurposeSetting, "", "saved", "settings", false, true, "old-value")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, secrets.KeySize)))
	t.Setenv(previousKeyEnv, base64.StdEncoding.EncodeToString(oldKey))
	if got, err := openRow(t, loadRing(t).For(st.AsOperator()), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("opened %q, %v under the previous key; want abc", got, err)
	}
	c := loadRing(t).For(st.AsOperator())
	if _, err := c.OpenBoundCurrent(secrets.PurposeSetting, "", "saved", "settings", false, true, bound); err == nil || errors.Is(err, secrets.ErrKeyRefused) {
		t.Fatalf("current-key miss prevents rotation fallback: %v", err)
	}
	if got, err := c.OpenBound(secrets.PurposeSetting, "", "saved", "settings", false, true, bound); err != nil || got != "old-value" {
		t.Fatalf("previous-key fallback: %q, %v", got, err)
	}
}

func TestKeyring_OpenWithoutAKeySaysSo(t *testing.T) {
	keyFileIn(t, t.TempDir())
	if _, err := loadRing(t).For(nil).Open("enc:v1:AAAA"); !errors.Is(err, errNoKey) {
		t.Fatalf("open with no key = %v, want ErrNoKey", err)
	}
}

func TestKeyring_ReadsAKeyAnotherProcessCreatedLater(t *testing.T) {
	keyFileIn(t, t.TempDir())
	st := openStore(t)
	reader := loadRing(t)
	sealRow(t, loadRing(t).For(st.AsOperator()), st, "TOKEN", "abc")
	if got, err := openRow(t, reader.For(st.AsOperator()), st, "TOKEN"); err != nil || got != "abc" {
		t.Errorf("a keyring loaded before the key existed opened %q, %v; want abc", got, err)
	}
}

func TestKeyring_RefusesAKeyFileOthersCanWrite(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{5}, secrets.KeySize), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(KeyringOptions{}); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("load with a group-writable key file = %v, want a writable-target refusal", err)
	}
}

func TestKeyring_RefusesAKeyFileOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		path := keyFileIn(t, t.TempDir())
		if err := os.WriteFile(path, bytes.Repeat([]byte{5}, secrets.KeySize), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := LoadKeyring(KeyringOptions{})
		if err == nil || !strings.Contains(err.Error(), "chmod 600 "+path) {
			t.Fatalf("load with a %o key file = %v, want a refusal naming chmod 600", mode, err)
		}
	}
}

func TestKeyring_RefusesAKeyFileSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.key")
	if err := os.WriteFile(target, bytes.Repeat([]byte{6}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	path := keyFileIn(t, dir)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyring(KeyringOptions{}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("load through a symlinked key file = %v, want a symlink refusal", err)
	}
}

func TestKeyring_OnlyACreatingProcessMakesTheKey(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	st := openStore(t)
	ring, err := LoadKeyring(KeyringOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ring.For(st.AsOperator()).Seal("value")
	if !errors.Is(err, errNoKeyToCreate) || !errors.Is(err, secrets.ErrKeyRefused) || !strings.Contains(err.Error(), keyEnv) {
		t.Fatalf("seal in a non-creating process = %v, want ErrNoKeyToCreate naming %s", err, keyEnv)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a non-creating process created %s", path)
	}

	sealRow(t, loadRing(t).For(st.AsOperator()), st, "TOKEN", "abc")
	if got, err := openRow(t, ring.For(st.AsOperator()), st, "TOKEN"); err != nil || got != "abc" {
		t.Fatalf("the non-creating process opened %q, %v once the key existed; want abc", got, err)
	}
}

func TestKeyringOpenFailureNamesSelectedKey(t *testing.T) {
	st := openStore(t)
	keyFileIn(t, t.TempDir())
	original := loadRing(t).For(st.AsOperator())
	legacy, err := original.Seal("private-value")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := original.SealBound(secrets.PurposeSecret, "local", "TOKEN", "", true, true, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{9}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	wrong := loadRing(t).For(st.AsOperator())
	for _, open := range []func() (string, error){
		func() (string, error) { return wrong.Open(legacy) },
		func() (string, error) {
			return wrong.OpenBound(secrets.PurposeSecret, "local", "TOKEN", "", true, true, bound)
		},
		func() (string, error) {
			return wrong.OpenBoundCurrent(secrets.PurposeSecret, "local", "TOKEN", "", true, true, bound)
		},
	} {
		_, err := open()
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), KeyFileEnv) || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("key failure lost its private repair: %v", err)
		}
	}
}

func TestKeyringNewHomesCreateTheirOwnKeys(t *testing.T) {
	user := t.TempDir()
	testhome.Set(t, user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv(KeyFileEnv, "")
	t.Setenv(keyEnv, "")
	t.Setenv(previousKeyEnv, "")
	legacyPath := filepath.Join(user, "config", "sparkwing", "secrets.key")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyKey := bytes.Repeat([]byte{7}, secrets.KeySize)
	if err := os.WriteFile(legacyPath, legacyKey, 0o600); err != nil {
		t.Fatal(err)
	}
	var keys [][]byte
	for range 2 {
		home := t.TempDir()
		t.Setenv("SPARKWING_HOME", home)
		sealed, err := loadRing(t).For(nil).Seal("private-value")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "secrets.key")
		key, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("new home did not create its own key: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(key) != secrets.KeySize || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 || bytes.Equal(key, legacyKey) {
			t.Fatal("new home reused a key or created a nonprivate key")
		}
		if got, err := loadRing(t).For(nil).Open(sealed); err != nil || got != "private-value" {
			t.Fatalf("existing home did not reopen: %q, %v", got, err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(key, unchanged) {
			t.Fatalf("existing home changed its key: %v", err)
		}
		keys = append(keys, key)
	}
	if bytes.Equal(keys[0], keys[1]) {
		t.Fatal("separate homes share a key")
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(unchanged, legacyKey) {
		t.Fatalf("changed legacy key: %v", err)
	}
}

func TestKeyringDefaultHomeKeepsLegacyConfigKey(t *testing.T) {
	user := t.TempDir()
	testhome.Set(t, user)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, "config"))
	t.Setenv("SPARKWING_HOME", filepath.Join(user, ".sparkwing"))
	t.Setenv(KeyFileEnv, "")
	t.Setenv(keyEnv, "")
	t.Setenv(previousKeyEnv, "")
	path := filepath.Join(user, "config", "sparkwing", "secrets.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{4}, secrets.KeySize)
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal("existing-value")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := loadRing(t).For(nil).Open(sealed); err != nil || got != "existing-value" {
		t.Fatalf("existing default home lost its key: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(user, ".sparkwing", "secrets.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrated existing key: %v", err)
	}
}

func TestKeyringDefaultHomeRetainsConfigKeyWhenLocalFileExists(t *testing.T) {
	user := t.TempDir()
	testhome.Set(t, user)
	t.Setenv("APPDATA", filepath.Join(user, "AppData", "Roaming"))
	t.Setenv("SPARKWING_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(KeyFileEnv, "")
	t.Setenv(keyEnv, "")
	t.Setenv(previousKeyEnv, "")
	legacy, err := fssecure.ConfigFile("secrets.key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	configKey := bytes.Repeat([]byte{1}, secrets.KeySize)
	if err := os.WriteFile(legacy, configKey, 0o600); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(user, ".sparkwing", "secrets.key")
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		t.Fatal(err)
	}
	localKey := bytes.Repeat([]byte{2}, secrets.KeySize)
	if err := os.WriteFile(local, localKey, 0o600); err != nil {
		t.Fatal(err)
	}
	ring := loadRing(t)
	if ring.path != legacy {
		t.Fatalf("default key changed without any selectors: selected %s, existing config key is %s", ring.path, legacy)
	}
	old, err := secrets.NewCipher(configKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal("existing-value")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ring.For(nil).Open(sealed); err != nil || got != "existing-value" {
		t.Fatalf("default home lost its existing key: %q, %v", got, err)
	}
	sealed, err = ring.For(nil).Seal("new-value")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := old.Open(sealed); err != nil || got != "new-value" {
		t.Fatalf("default home sealed under a second key: %q, %v", got, err)
	}
	t.Setenv(KeyFileEnv, local)
	if _, err := loadRing(t).For(nil).Open(sealed); err == nil || strings.Contains(err.Error(), "no longer falls back") {
		t.Fatalf("explicit selector gave the default home a false migration repair: %v", err)
	}
	for path, want := range map[string][]byte{legacy: configKey, local: localKey} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("changed existing key %s: %v", path, err)
		}
	}
}

func TestKeyringFailureRepairFollowsKeySource(t *testing.T) {
	for _, source := range []string{"inline", "store"} {
		t.Run(source, func(t *testing.T) {
			path := keyFileIn(t, t.TempDir())
			originalKey := bytes.Repeat([]byte{1}, secrets.KeySize)
			if err := os.WriteFile(path, originalKey, 0o600); err != nil {
				t.Fatal(err)
			}
			sealed, err := loadRing(t).For(nil).Seal("private-value")
			if err != nil {
				t.Fatal(err)
			}
			const credentialRepair = "restore the original secrets-key credential and restart"
			var wrong *Keyring
			if source == "inline" {
				t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, secrets.KeySize)))
				wrong = loadRing(t)
			} else {
				if err := os.WriteFile(path, bytes.Repeat([]byte{2}, secrets.KeySize), 0o600); err != nil {
					t.Fatal(err)
				}
				wrong, err = NewStoreKeyring(path, nil, credentialRepair)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = wrong.For(nil).Open(sealed)
			if err == nil || strings.Contains(err.Error(), KeyFileEnv) || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("key failure suggested an ineffective selector or leaked the value: %v", err)
			}
			if source == "inline" {
				if !strings.Contains(err.Error(), keyEnv) || !strings.Contains(err.Error(), "unset "+keyEnv) {
					t.Fatalf("inline failure lost its repair: %v", err)
				}
				t.Setenv(keyEnv, "")
				if got, err := loadRing(t).For(nil).Open(sealed); err != nil || got != "private-value" {
					t.Fatalf("inline repair failed: %q, %v", got, err)
				}
			} else if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), credentialRepair) {
				t.Fatalf("store key failure lost its credential repair: %v", err)
			}
		})
	}
}

func TestKeyringOpenFailureBlamesTheKeyOnlyForKeyMisses(t *testing.T) {
	path := keyFileIn(t, t.TempDir())
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign, err := secrets.NewCipher(bytes.Repeat([]byte{2}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := foreign.SealBound(secrets.PurposeSecret, "default", "NAME", "", true, false, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	c := loadRing(t).For(nil)
	_, err = c.OpenBound(secrets.PurposeSecret, "default", "NAME", "", true, false, sealed)
	var diagnostic *apierr.Error
	if !errors.As(err, &diagnostic) || !strings.Contains(diagnostic.Message, path) || !strings.Contains(diagnostic.Hint, KeyFileEnv) {
		t.Fatalf("a key miss lost its named-key diagnostic: %v", err)
	}
	for name, open := range map[string]func() error{
		"bad encoding": func() error {
			_, err := c.OpenBound(secrets.PurposeSecret, "default", "NAME", "", true, false, secrets.BoundPrefix+"!!!")
			return err
		},
		"unknown purpose": func() error {
			_, err := c.OpenBound(secrets.Purpose("unknown"), "default", "NAME", "", true, false, sealed)
			return err
		},
	} {
		err := open()
		if err == nil || errors.As(err, &diagnostic) || strings.Contains(err.Error(), KeyFileEnv) ||
			strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), path) {
			t.Errorf("%s: a failure that is not the key's carries a key repair: %v", name, err)
		}
	}
}

func TestStoreKeyringKeepsKeyDetailOutOfClientDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, secrets.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	const restore = "Restore that key to the controller volume"
	ring, err := NewStoreKeyring(path, nil, restore)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := secrets.NewCipher(bytes.Repeat([]byte{2}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := foreign.SealBound(secrets.PurposeSecret, "default", "NAME", "", true, false, "private-value")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ring.For(nil).OpenBound(secrets.PurposeSecret, "default", "NAME", "", true, false, sealed)
	var diagnostic *apierr.Error
	if err == nil || errors.As(err, &diagnostic) {
		t.Fatalf("a controller keyring returned a client-facing key diagnostic: %v", err)
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), restore) || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("a controller keyring lost the operator detail its server logs: %v", err)
	}
}
