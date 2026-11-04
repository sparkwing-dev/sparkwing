package cluster

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// goSeedRoot is where build/Dockerfile.runner primes, per SDK version, a module cache and a build cache.
const goSeedRoot = "/usr/local/share/sparkwing/go-seed"

// seedGoCaches copies the image's primed caches for version into empty cache paths.
//
// perf: a Cloud plan pins the runner's own SDK version, so the seed turns its
// SDK download and compile into a copy. The root filesystem is read-only, so
// Go cannot use the seed in place; an existing cache is left alone.
func seedGoCaches(root, version, modules, gocache string) error {
	var errs []error
	for _, cache := range []struct{ seed, target string }{{"modules", modules}, {"gocache", gocache}} {
		source := filepath.Join(root, version, cache.seed)
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

// copyTree copies regular files and directories; directories stay writable so go can add to the cache.
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
