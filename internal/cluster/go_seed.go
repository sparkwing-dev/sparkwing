package cluster

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// safety: build/Dockerfile.runner primes these content-addressed caches with the image's toolchain.
const (
	goSeedRoot     = "/usr/local/share/sparkwing/go-seed"
	goStandardSeed = "/usr/local/share/sparkwing/go-stdlib"
)

// perf: the image is read-only, so Go needs writable copies of its primed caches.
// Standard-library cache keys let versions without an SDK seed reuse compiler work too.
func seedGoCaches(root, standard, version, modules, gocache string) error {
	var errs []error
	for _, cache := range []struct{ seed, target string }{{"modules", modules}, {"gocache", gocache}} {
		source := filepath.Join(root, version, cache.seed)
		if cache.seed == "gocache" && !exists(source) {
			source = standard
		}
		if cache.target == "" || !exists(source) || exists(cache.target) {
			continue
		}
		// safety: a partly copied module directory would look complete to go, so the cache appears only whole.
		partial := cache.target + ".seeding"
		if err := os.RemoveAll(partial); err != nil {
			errs = append(errs, err)
			continue
		}
		err := copyTree(source, partial)
		if err == nil {
			err = os.Rename(partial, cache.target)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("seed %s: %w", cache.target, errors.Join(err, os.RemoveAll(partial))))
		}
	}
	return errors.Join(errs...)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// safety: copied directories stay writable so go can add to the cache.
func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		switch {
		case entry.IsDir():
			return os.Mkdir(destination, 0o755)
		case entry.Type().IsRegular():
			return copyFile(path, destination)
		default:
			return fmt.Errorf("go cache seed holds %s, which is not a regular file", path)
		}
	})
}

func copyFile(source, target string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
