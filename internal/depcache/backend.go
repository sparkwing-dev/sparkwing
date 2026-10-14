package depcache

import (
	"context"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"io"
	"os"
	"path/filepath"
)

// Backend stores dependency archives. Hosts supply remote implementations explicitly.
type Backend interface {
	Label() string
	Exists(context.Context, string) (bool, error)
	Fetch(context.Context, string, string) (int64, error)
	Store(context.Context, string, string) (int64, error)
}

type backendKey struct{}

// WithBackend binds a host-provided backend to one run without ambient credentials.
func WithBackend(ctx context.Context, backend Backend) context.Context {
	return context.WithValue(ctx, backendKey{}, backend)
}

func selectBackend(ctx context.Context) Backend {
	if backend, ok := ctx.Value(backendKey{}).(Backend); ok {
		return backend
	}
	return &localBackend{}
}

type localBackend struct{}

func (l *localBackend) Label() string { return "local" }

func (l *localBackend) archivePath(key string) (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Root, "depcache", key+".tar.gz"), nil
}

func (l *localBackend) Exists(_ context.Context, key string) (bool, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (l *localBackend) Fetch(_ context.Context, key, dir string) (int64, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if err := ExtractArchiveStaged(f, dir); err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (l *localBackend) Store(_ context.Context, key, dir string) (int64, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return 0, err
	}
	if err := fssecure.EnsureDir(filepath.Dir(p)); err != nil {
		return 0, err
	}
	// safety: temp-then-rename keeps a concurrent reader off a
	// half-written archive.
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+key+"-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := WriteArchive(tmp, dir); err != nil {
		tmp.Close()
		return 0, err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, p); err != nil {
		return 0, err
	}
	return size, nil
}
