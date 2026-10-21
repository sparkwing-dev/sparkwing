package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func epochPath(dir string) string { return filepath.Join(dir, "credential-epoch") }

// LoadOrCreateEpoch loads the external credential fence, creating it on first startup.
// A restored database must retain this file from the current deployment.
func LoadOrCreateEpoch(dir string) (string, error) {
	path := epochPath(dir)
	body, err := os.ReadFile(path)
	if err == nil {
		return parseEpoch(body)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	epoch, err := newEpoch()
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateEpoch(dir)
	}
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(epoch + "\n")
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return "", err
	}
	return epoch, fssecure.SyncDir(dir)
}

// RotateEpoch invalidates every bearer and session from before an offline restore.
// Stop the controller before replacing its database or rotating this fence.
func RotateEpoch(dir string) (string, error) {
	epoch, err := newEpoch()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "..credential-epoch-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(epoch + "\n")
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), epochPath(dir)); err != nil {
		return "", err
	}
	return epoch, fssecure.SyncDir(dir)
}

func newEpoch() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func parseEpoch(body []byte) (string, error) {
	epoch := strings.TrimSpace(string(body))
	b, err := hex.DecodeString(epoch)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("credential-epoch must contain 32 hex-encoded random bytes")
	}
	return epoch, nil
}
