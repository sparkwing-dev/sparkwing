package depcache

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Reader restores archives without granting permission to publish them.
type Reader interface {
	Exists(context.Context, string) (bool, error)
	Fetch(context.Context, string, string) (int64, error)
}

// Backend stores dependency archives. Hosts supply remote implementations explicitly.
type Backend interface {
	Reader
	Label() string
	Store(context.Context, string, string) (int64, error)
}

type backendKey struct{}

// WithBackend binds a host-provided backend to one run without ambient credentials.
func WithBackend(ctx context.Context, backend Backend) context.Context {
	return context.WithValue(ctx, backendKey{}, backend)
}

// WithRemoteReader lets a local run restore an opted-in remote cache after a
// local miss. Saves remain local; the remote binding has no write method.
func WithRemoteReader(ctx context.Context, remote Reader) context.Context {
	if remote == nil {
		return ctx
	}
	return WithBackend(ctx, &localWithRemote{remote: remote})
}

type localWithRemote struct {
	localBackend
	remote Reader
}

func (b localWithRemote) Label() string { return "local+remote" }

func (b localWithRemote) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if found, err := b.localBackend.Exists(ctx, key); err != nil || found {
		return found, err
	}
	return b.remote.Exists(ctx, key)
}

func (b localWithRemote) Fetch(ctx context.Context, key, dir string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if found, err := b.localBackend.Exists(ctx, key); err != nil {
		return 0, err
	} else if found {
		return b.localBackend.Fetch(ctx, key, dir)
	}
	return b.remote.Fetch(ctx, key, dir)
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

func (l *localBackend) Fetch(ctx context.Context, key, dir string) (int64, error) {
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
	if err := extractArchiveStaged(ctx, f, dir); err != nil {
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
