package sparkwing

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var maxExtractBytes = int64(20 << 30)

type tarExtractPolicy struct {
	allowSymlinks bool
	minDirPerm    fs.FileMode
	minFilePerm   fs.FileMode
	rename        func(name string) (string, bool)
}

func extractTarInRoot(tr *tar.Reader, dir string, policy tarExtractPolicy) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var deferredDirModes []dirMode
	var written int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		name := hdr.Name
		if policy.rename != nil {
			kept := false
			if name, kept = policy.rename(name); !kept {
				continue
			}
		}
		rel, err := secureArchiveRel(name)
		if err != nil {
			return err
		}
		if rel == "." {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			// safety: a read-only directory mode would block extracting
			// its children; create writable, restore the mode afterwards.
			if err := root.MkdirAll(rel, provisionalDirPerm); err != nil {
				return err
			}
			deferredDirModes = append(deferredDirModes, dirMode{rel, hdr.FileInfo().Mode().Perm() | policy.minDirPerm})

		case tar.TypeReg:
			if err := mkdirParent(root, rel); err != nil {
				return err
			}
			// safety: overwriting a read-only file from a previous
			// partial restore needs the remove first.
			_ = root.Remove(rel)
			f, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm()|policy.minFilePerm)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, maxExtractBytes-written+1))
			written += n
			if err != nil {
				f.Close()
				return err
			}
			if written > maxExtractBytes {
				f.Close()
				return fmt.Errorf("archive exceeds the %s extraction limit; refusing to fill the disk", humanBytes(maxExtractBytes))
			}
			if err := f.Close(); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if !policy.allowSymlinks {
				continue
			}
			if err := symlinkStaysInside(dir, filepath.Join(dir, rel), hdr.Linkname); err != nil {
				return err
			}
			if err := mkdirParent(root, rel); err != nil {
				return err
			}
			_ = root.Remove(rel)
			if err := root.Symlink(hdr.Linkname, rel); err != nil {
				return err
			}

		default:
		}
	}

	// safety: deepest-first restores nested read-only directories
	// without locking out their own just-extracted contents.
	for i := len(deferredDirModes) - 1; i >= 0; i-- {
		dm := deferredDirModes[i]
		_ = root.Chmod(dm.path, dm.mode)
	}
	return nil
}

const provisionalDirPerm = fs.FileMode(0o700)

func mkdirParent(root *os.Root, rel string) error {
	parent := filepath.Dir(rel)
	if parent == "." {
		return nil
	}
	return root.MkdirAll(parent, provisionalDirPerm)
}

func secureArchiveRel(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return clean, nil
}

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
