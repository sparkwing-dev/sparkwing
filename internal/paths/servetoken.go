package paths

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const serveTokenBytes = 32

// ServeTokenFile is the owner-only file holding the bearer `sparkwing serve`
// requires on its TCP listener.
func (p Paths) ServeTokenFile() string { return filepath.Join(p.Root, "serve-token") }

// ServeToken reads the local dashboard's bearer. The error wraps
// [os.ErrNotExist] when no dashboard has created one under this home.
func (p Paths) ServeToken() (string, error) {
	f, err := fssecure.OpenFile(p.ServeTokenFile(), os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4*serveTokenBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.ServeTokenFile(), err)
	}
	token := strings.TrimSpace(string(raw))
	if decoded, err := hex.DecodeString(token); err != nil || len(decoded) != serveTokenBytes {
		return "", fmt.Errorf("%s does not hold a serve token; delete it and restart the dashboard", p.ServeTokenFile())
	}
	return token, nil
}

// EnsureServeToken returns the local dashboard's bearer, creating it on
// first use. It stays the same across restarts, so a browser keeps the
// session cookie it was given.
func (p Paths) EnsureServeToken() (string, error) {
	token, err := p.ServeToken()
	if !errors.Is(err, os.ErrNotExist) {
		return token, err
	}
	if err := p.EnsureRoot(); err != nil {
		return "", err
	}
	raw := make([]byte, serveTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(p.Root, ".serve-token-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.WriteString(hex.EncodeToString(raw) + "\n")
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	// safety: a link fails when the name exists, so two dashboards starting
	// at once agree on whichever token landed first rather than each
	// overwriting the other's.
	if err := os.Link(tmp.Name(), p.ServeTokenFile()); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create %s: %w", p.ServeTokenFile(), err)
	}
	return p.ServeToken()
}
