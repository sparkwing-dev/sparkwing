// Package localsecrets keeps this machine's secrets in the local runs store,
// sealed with the controller's cipher under a key that lives in the user
// config directory rather than in SPARKWING_HOME, so a copied or backed-up
// state.db carries only ciphertext.
//
// Every local process that seals or opens a stored secret builds its cipher
// here: the admission daemon's controller API, the dashboard (`sparkwing serve`), a run's
// loopback controller, and a run that reads the store without a daemon.
package localsecrets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/configguard"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// KeyEnv carries the key as base64 of 32 bytes, as it does for
	// sparkwing-controller. It overrides the key file.
	KeyEnv = "SPARKWING_SECRETS_KEY"
	// PreviousKeyEnv carries a retired key that values may still be sealed
	// under, until `sparkwing secrets rotate` reseals them.
	PreviousKeyEnv = "SPARKWING_SECRETS_PREVIOUS_KEY"
	// KeyFileEnv names the key file in place of secrets.key in the config
	// directory.
	KeyFileEnv = "SPARKWING_SECRETS_KEY_FILE"

	keyFileName = "secrets.key"

	sealedProbeTimeout = 10 * time.Second
)

// ErrNoKey reports a stored secret that cannot be opened because this
// machine has no secrets key yet.
var ErrNoKey = errors.New("no local secrets key")

// KeyPath reports where this machine's secrets key file lives:
// $SPARKWING_SECRETS_KEY_FILE when set, else secrets.key in the sparkwing
// config directory.
func KeyPath() (string, error) {
	if v := os.Getenv(KeyFileEnv); v != "" {
		return v, nil
	}
	// safety: a test binary that sets neither variable would otherwise mint a
	// key in the developer's own config directory.
	if os.Getenv("XDG_CONFIG_HOME") == "" && paths.UnderTest() {
		return filepath.Join(paths.TestSandbox(), "config", "sparkwing", keyFileName), nil
	}
	return fssecure.ConfigFile(keyFileName)
}

// ErrNoKeyToCreate reports a seal in a process that may not create the
// machine's key; only the sparkwing daemon creates it.
var ErrNoKeyToCreate = errors.New("no local secrets key")

// KeyringOptions choose how [LoadKeyring] treats this process.
type KeyringOptions struct {
	// Create lets the first seal create the key file. Only the process that
	// owns the machine's store may: the daemon, or a run on a machine with
	// no daemon at all.
	Create bool
}

// Keyring holds this process's view of the machine's secrets key. The key
// comes from [KeyEnv] or the key file; a keyring with neither creates the
// file on its first seal when [KeyringOptions.Create] allows it.
type Keyring struct {
	path      string
	fromEnv   bool
	mayCreate bool
	previous  []byte
	restore   string

	mu     sync.Mutex
	cipher *secrets.Cipher
}

// LoadKeyring reads the key from [KeyEnv], or from the key file when the
// variable is unset; a missing file is not an error.
func LoadKeyring(opts KeyringOptions) (*Keyring, error) {
	path, err := KeyPath()
	if err != nil {
		return nil, err
	}
	current, err := decodeEnvKey(KeyEnv, os.Getenv("SPARKWING_SECRETS_KEY"))
	if err != nil {
		return nil, err
	}
	previous, err := decodeEnvKey(PreviousKeyEnv, os.Getenv("SPARKWING_SECRETS_PREVIOUS_KEY"))
	if err != nil {
		return nil, err
	}
	k := &Keyring{path: path, previous: previous, fromEnv: current != nil, mayCreate: opts.Create}
	if current == nil {
		current, err = readKeyFile(path)
		if err != nil {
			return nil, err
		}
	}
	if current == nil {
		if previous != nil {
			return nil, fmt.Errorf("%s is set without a current key; set %s, or keep %s, to the key values are sealed under now",
				PreviousKeyEnv, KeyEnv, path)
		}
		return k, nil
	}
	if k.cipher, err = newCipher(current, previous); err != nil {
		return nil, err
	}
	return k, nil
}

// NewStoreKeyring returns a keyring whose key lives in the file at path,
// beside the store it seals for, and is created there on the first seal.
// previous opens values sealed under an older key. restore is what a refusal
// tells the operator to do when the store already holds values sealed under a
// key path does not hold, such as where the original key belongs.
func NewStoreKeyring(path string, previous []byte, restore string) (*Keyring, error) {
	k := &Keyring{path: path, previous: previous, mayCreate: true, restore: restore}
	current, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	if current != nil {
		if k.cipher, err = newCipher(current, previous); err != nil {
			return nil, err
		}
	}
	return k, nil
}

func decodeEnvKey(name, v string) ([]byte, error) {
	if v == "" {
		return nil, nil
	}
	key, err := secrets.DecodeKey(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return key, nil
}

func newCipher(current, previous []byte) (*secrets.Cipher, error) {
	if previous == nil {
		return secrets.NewCipher(current)
	}
	return secrets.NewCipherWithPrevious(current, previous)
}

// safety: the key opens every local secret, so it is read only from an
// owner-only regular file that is not a symlink and did not change while it
// was opened.
func readKeyFile(path string) ([]byte, error) {
	f, err := fssecure.OpenPrivateConfig(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the local secrets key: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "sparkwing: close %s: %v\n", path, cerr)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(f, secrets.KeySize+1))
	if err != nil {
		return nil, fmt.Errorf("read the local secrets key: %w", err)
	}
	if len(data) != secrets.KeySize {
		return nil, fmt.Errorf("the local secrets key %s holds %d bytes; it must hold exactly %d", path, len(data), secrets.KeySize)
	}
	return data, nil
}

// Path reports the key file this keyring reads and creates.
func (k *Keyring) Path() string { return k.path }

// For returns the cipher the controller serving st seals and opens secrets
// with. st is what the cipher asks, before it creates a key, whether any
// value is already sealed under another one; nil skips that question.
func (k *Keyring) For(st *store.Store) *Cipher {
	return &Cipher{ring: k, st: st}
}

// safety: another process may have created the key file since this one
// started, so a keyring without a key reads the file again.
func (k *Keyring) loaded() (*secrets.Cipher, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cipher != nil {
		return k.cipher, nil
	}
	key, err := readKeyFile(k.path)
	if err != nil || key == nil {
		return nil, err
	}
	if k.cipher, err = newCipher(key, k.previous); err != nil {
		return nil, err
	}
	return k.cipher, nil
}

// safety: the file is written whole under a private name and linked into
// place, and a link never replaces an existing file, so two processes
// creating it at once both end up with whichever key linked first.
func (k *Keyring) ensure(ctx context.Context, st *store.Store) (*secrets.Cipher, error) {
	if c, err := k.loaded(); c != nil || err != nil {
		return c, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cipher != nil {
		return k.cipher, nil
	}
	if err := k.missingLocked(ctx, st); err != nil {
		return nil, err
	}
	if !k.mayCreate {
		return nil, fmt.Errorf("%w: %s does not exist, and only the sparkwing daemon creates it. Store the first "+
			"local secret with `sparkwing secrets set`, which starts the daemon; if the daemon runs with %s, "+
			"set it for this process too%.0w", ErrNoKeyToCreate, k.path, KeyEnv, secrets.ErrKeyRefused)
	}
	// safety: a store keyring's file sits beside the database the process
	// already writes, so only the machine key in the config directory is guarded.
	if k.restore == "" {
		if err := configguard.GuardWrite("the local secrets key", KeyFileEnv, k.path); err != nil {
			return nil, fmt.Errorf("%w%.0w", err, secrets.ErrKeyRefused)
		}
	}
	key, err := k.create()
	if err != nil {
		return nil, err
	}
	if k.cipher, err = newCipher(key, k.previous); err != nil {
		return nil, err
	}
	return k.cipher, nil
}

func (k *Keyring) create() ([]byte, error) {
	dir := filepath.Dir(k.path)
	if err := fssecure.EnsureConfigDir(dir); err != nil {
		return nil, fmt.Errorf("prepare %s: %w", dir, err)
	}
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate the local secrets key: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+keyFileName+"-*")
	if err != nil {
		return nil, fmt.Errorf("create the local secrets key: %w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sparkwing: remove %s: %v\n", tmp.Name(), err)
		}
	}()
	werr := tmp.Chmod(0o600)
	if werr == nil {
		_, werr = tmp.Write(key)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, fmt.Errorf("write the local secrets key: %w", werr)
	}
	if err := os.Link(tmp.Name(), k.path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("install the local secrets key at %s: %w; creating it safely needs a filesystem "+
				"with hard links, so set %s or point %s at a local disk", k.path, err, KeyEnv, KeyFileEnv)
		}
		winner, rerr := readKeyFile(k.path)
		if rerr != nil {
			return nil, rerr
		}
		if winner == nil {
			return nil, fmt.Errorf("the local secrets key at %s vanished while it was being created", k.path)
		}
		return winner, nil
	}
	if err := syncDir(dir); err != nil {
		return nil, fmt.Errorf("persist the local secrets key at %s: %w", k.path, err)
	}
	return key, nil
}

// safety: a crash after the link but before the directory entry reaches disk
// would lose the key while values sealed under it survive in state.db.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	if serr != nil && runtime.GOOS == "windows" {
		return nil
	}
	return serr
}

// MissingKey reports why this keyring must not create a key for st: st
// already holds values sealed under a key it does not have, and a second key
// would leave some rows only one key opens. It returns nil once a key is
// known or when st holds nothing sealed.
func (k *Keyring) MissingKey(ctx context.Context, st *store.Store) error {
	if c, err := k.loaded(); c != nil || err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.missingLocked(ctx, st)
}

func (k *Keyring) missingLocked(ctx context.Context, st *store.Store) error {
	if k.cipher != nil || st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sealedProbeTimeout)
	defer cancel()
	sealed, err := st.SampleSealedSecrets(ctx, "enc:", 1)
	if err != nil {
		return fmt.Errorf("check the local secrets store for sealed values: %w", err)
	}
	if len(sealed) == 0 {
		return nil
	}
	if k.restore != "" {
		return fmt.Errorf("%w: the secrets store holds values sealed under a key this process does not have, "+
			"so it will not create a new one at %s. %s%.0w", ErrNoKey, k.path, k.restore, secrets.ErrKeyRefused)
	}
	return fmt.Errorf("%w: the local secrets store holds values sealed under a key this process does not have, "+
		"so it will not create a new one. Restore that key to %s, or set %s to it (base64 of its 32 bytes) "+
		"in the environment the sparkwing daemon starts in, then run `sparkwing daemon restart`%.0w",
		ErrNoKey, k.path, KeyEnv, secrets.ErrKeyRefused)
}
