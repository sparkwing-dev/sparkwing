package wingd

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func writeAtomicFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := fssecure.OpenFile(filepath.Join(dir, "."+filepath.Base(path)+"-"+rand.Text()+".tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("wingd: temp %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errors.Join(fmt.Errorf("wingd: write %s: %w", path, err), os.Remove(tmpName))
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errors.Join(fmt.Errorf("wingd: sync %s: %w", path, err), os.Remove(tmpName))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(fmt.Errorf("wingd: close %s: %w", path, err), os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Join(fmt.Errorf("wingd: rename %s: %w", path, err), os.Remove(tmpName))
	}
	if err := syncStateDirectory(dir); err != nil {
		return err
	}
	return nil
}
