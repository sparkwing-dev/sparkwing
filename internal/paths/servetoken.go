package paths

import (
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

const serveTokenBytes = 32

// serveTokenFile is the owner-only file holding the bearer `sparkwing serve`
// requires on its TCP listener.
func (p Paths) serveTokenFile() string { return filepath.Join(p.Root, "serve-token") }

// serveToken reads the local dashboard's bearer. The error wraps
// [os.ErrNotExist] when no dashboard has created one under this home.
func (p Paths) serveToken() (string, error) {
	f, err := fssecure.OpenCredential(p.Root, "serve-token")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4*serveTokenBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", p.serveTokenFile(), err)
	}
	token := strings.TrimSpace(string(raw))
	if decoded, err := hex.DecodeString(token); err != nil || len(decoded) != serveTokenBytes {
		return "", fmt.Errorf("%s does not hold a serve token; delete it and restart the dashboard", p.serveTokenFile())
	}
	return token, nil
}
