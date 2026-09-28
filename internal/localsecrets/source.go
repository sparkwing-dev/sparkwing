package localsecrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const readTimeout = 30 * time.Second

// SocketSource resolves a local run's secrets through the admission daemon's
// controller API, which holds the key: c is a client over the daemon's API
// socket and runID the run the read is for.
//
// The daemon answers a socket caller as the machine's admin, so a read
// resolves the run's pipeline row and falls back to the unscoped row whether
// or not that row is shared.
func SocketSource(ctx context.Context, c *client.Client, runID string) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		ctx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		sec, err := c.GetSecretForRun(ctx, name, runID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", false, secrets.ErrSecretMissing
			}
			return "", false, err
		}
		return sec.Value, sec.Masked, nil
	})
}

// StoreSource resolves secrets for pipeline by reading the runs store at
// dbPath directly and opening each value with this machine's key. It is the
// path for a run no daemon hosts. Each read opens the store read-only and
// closes it again; a missing store holds no secrets. The read matches the
// daemon's: the pipeline's row, else the unscoped row.
func StoreSource(dbPath, pipeline string) secrets.Source {
	s := &storeSource{path: dbPath, pipeline: pipeline}
	return secrets.SourceFunc(s.read)
}

type storeSource struct {
	path     string
	pipeline string

	once    sync.Once
	cipher  *Cipher
	ringErr error
}

func (s *storeSource) read(name string) (_ string, _ bool, err error) {
	if _, err := os.Stat(s.path); errors.Is(err, fs.ErrNotExist) {
		return "", false, secrets.ErrSecretMissing
	}
	st, err := store.OpenReadOnly(s.path)
	if err != nil {
		return "", false, fmt.Errorf("open the local secrets store %s: %w", s.path, err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close the local secrets store %s: %w", s.path, cerr)
		}
	}()
	sec, err := st.GetSecretForPipeline(name, s.pipeline)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, secrets.ErrSecretMissing
	}
	if err != nil {
		return "", false, fmt.Errorf("read secret %s: %w", name, err)
	}
	s.once.Do(func() {
		ring, err := LoadKeyring(KeyringOptions{})
		if err != nil {
			s.ringErr = err
			return
		}
		s.cipher = ring.For(nil)
	})
	if s.ringErr != nil {
		return "", false, s.ringErr
	}
	value, err := controller.OpenSecretValue(s.cipher, store.DefaultTeam, sec)
	if err != nil {
		return "", false, fmt.Errorf("open secret %s: %w", name, err)
	}
	return value, sec.Masked, nil
}
