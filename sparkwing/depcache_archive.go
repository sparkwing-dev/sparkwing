package sparkwing

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeDepCacheArchive streams dir as a gzipped tar to w. Entries are
// recorded relative to dir. Regular files, directories, and symlinks
// are archived (pnpm trees are symlink farms); sockets, devices, and
// other irregular entries are skipped.
func writeDepCacheArchive(w io.Writer, dir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		var link string
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		case !info.Mode().IsRegular() && !info.IsDir():
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// depCacheMaxExtractBytes bounds the total bytes an extract may write.
// The archives sparkwing produces are capped at
// remoteDepCacheMaxBytes compressed; a cluster cache entry that
// decompresses past this bound is a corrupt or hostile archive (a gzip
// bomb), and extraction stops rather than filling the runner's disk. A
// var so tests can shrink it.
var depCacheMaxExtractBytes = int64(20 << 30)

// extractDepCacheArchive expands a gzipped tar produced by
// writeDepCacheArchive into dir. Entry names are confined to dir:
// absolute names and names escaping via .. are rejected rather than
// written, and a symlink whose target leaves dir is rejected rather
// than created (so a later entry cannot be written through it).
// Extraction is bounded by depCacheMaxExtractBytes. Module caches ship
// read-only files, so directory modes are applied after their contents
// land.
func extractDepCacheArchive(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var deferredDirModes []dirMode
	var written int64

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		dest, err := securePathJoin(dir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			// safety: a read-only directory mode would block extracting
			// its children; create writable, restore the mode afterwards.
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			deferredDirModes = append(deferredDirModes, dirMode{dest, hdr.FileInfo().Mode().Perm()})

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			// safety: overwriting a read-only file from a previous
			// partial restore needs the remove first.
			_ = os.Remove(dest)
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, depCacheMaxExtractBytes-written+1))
			written += n
			if err != nil {
				f.Close()
				return err
			}
			if written > depCacheMaxExtractBytes {
				f.Close()
				return fmt.Errorf("archive exceeds the %s extraction limit; refusing to fill the disk", humanBytes(depCacheMaxExtractBytes))
			}
			if err := f.Close(); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if err := symlinkStaysInside(dir, dest, hdr.Linkname); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(hdr.Linkname, dest); err != nil {
				return err
			}

		default:
		}
	}

	// safety: deepest-first restores nested read-only directories
	// without locking out their own just-extracted contents.
	for i := len(deferredDirModes) - 1; i >= 0; i-- {
		dm := deferredDirModes[i]
		_ = os.Chmod(dm.path, dm.mode)
	}
	return nil
}

// extractDepCacheArchiveStaged extracts into a sibling temp directory
// and moves it into place only on full success, so a truncated stream
// leaves dir untouched instead of half-populated. Restore only runs
// against an absent or empty dir, so replacing it wholesale is safe.
func extractDepCacheArchiveStaged(r io.Reader, dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".depcache-restore-*")
	if err != nil {
		return err
	}
	if err := extractDepCacheArchive(r, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

// securePathJoin joins name under root, rejecting absolute names and
// names that escape root via "..".
func securePathJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return filepath.Join(root, clean), nil
}

// symlinkStaysInside rejects a symlink whose target resolves outside
// root. dest is the symlink's own path under root; linkname is its
// target as recorded in the archive. A rejected symlink is never
// created, which is what stops the plant-a-symlink-then-write-through-it
// escape: a later file entry has no out-of-tree link to follow.
func symlinkStaysInside(root, dest, linkname string) error {
	var target string
	if filepath.IsAbs(linkname) {
		target = filepath.Clean(linkname)
	} else {
		target = filepath.Clean(filepath.Join(filepath.Dir(dest), linkname))
	}
	rel, err := filepath.Rel(filepath.Clean(root), target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive symlink %q -> %q escapes the target directory", dest, linkname)
	}
	return nil
}
