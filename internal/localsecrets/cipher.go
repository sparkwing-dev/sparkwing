package localsecrets

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Cipher is the controller cipher for one local store. It is never nil where
// a controller takes it, so no local write stores a value as plaintext: the
// first seal on a machine with no key creates one. Opening needs a key that
// already exists and fails with [ErrNoKey] otherwise.
type Cipher struct {
	ring *Keyring
	st   *store.Store
}

var (
	_ controller.BoundCipher  = (*Cipher)(nil)
	_ controller.LegacyCipher = (*Cipher)(nil)
)

func (c *Cipher) Seal(plain string) (string, error) {
	inner, err := c.ring.ensure(context.Background(), c.st)
	if err != nil {
		return "", err
	}
	return inner.Seal(plain)
}

func (c *Cipher) Open(envelope string) (string, error) {
	inner, err := c.opener()
	if err != nil {
		return "", err
	}
	return inner.Open(envelope)
}

func (c *Cipher) SealBound(team, name, scope string, shared, masked bool, plain string) (string, error) {
	inner, err := c.ring.ensure(context.Background(), c.st)
	if err != nil {
		return "", err
	}
	return inner.SealBound(team, name, scope, shared, masked, plain)
}

func (c *Cipher) OpenBound(team, name, scope string, shared, masked bool, envelope string) (string, error) {
	inner, err := c.opener()
	if err != nil {
		return "", err
	}
	return inner.OpenBound(team, name, scope, shared, masked, envelope)
}

func (c *Cipher) OpenLegacy(name, scope string, shared, masked bool, envelope string) (string, error) {
	inner, err := c.opener()
	if err != nil {
		return "", err
	}
	return inner.OpenLegacy(name, scope, shared, masked, envelope)
}

// EnsureKey creates the machine's key when it has none, with the same refusal
// a seal gives, so a caller about to seal many values fails before sealing
// any of them.
func (c *Cipher) EnsureKey(ctx context.Context) error {
	_, err := c.ring.ensure(ctx, c.st)
	return err
}

func (c *Cipher) opener() (*secrets.Cipher, error) {
	inner, err := c.ring.loaded()
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, ErrNoKey
	}
	return inner, nil
}
