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

// safety: bounds one extraction, so a gzip bomb cannot fill the disk.
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
			// safety: chmod ignores the umask, so clamp the archive's mode
			// instead of letting it leave a world-writable directory.
			mode := hdr.FileInfo().Mode().Perm()&maxDirPerm | policy.minDirPerm
			deferredDirModes = append(deferredDirModes, dirMode{rel, mode})

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
			if err := mkdirParent(root, rel); err != nil {
				return err
			}
			if err := symlinkStaysInside(root, rel, hdr.Linkname); err != nil {
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

const (
	provisionalDirPerm = fs.FileMode(0o755)
	maxDirPerm         = fs.FileMode(0o755)
)

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

// safety: os.Root constrains where a link is created, never where it points,
// so measure the target from the entry's real parent, not its lexical one.
func symlinkStaysInside(root *os.Root, rel, linkname string) error {
	link := filepath.FromSlash(linkname)
	if link == "" || filepath.IsAbs(link) {
		return fmt.Errorf("archive symlink %q -> %q is not a relative link", rel, linkname)
	}
	if err := refuseSymlinkedAncestors(root, rel); err != nil {
		return err
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(rel), link))
	if target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive symlink %q -> %q escapes the target directory", rel, linkname)
	}
	return nil
}

// safety: a symlinked ancestor makes the entry's real depth shallower than its
// name, which is how a link that reads as contained lands outside the root.
func refuseSymlinkedAncestors(root *os.Root, rel string) error {
	parent := filepath.Dir(rel)
	if parent == "." {
		return nil
	}
	parts := strings.Split(parent, string(filepath.Separator))
	for i := range parts {
		ancestor := filepath.Join(parts[:i+1]...)
		fi, err := root.Lstat(ancestor)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q sits under the symlinked directory %q", rel, filepath.ToSlash(ancestor))
		}
	}
	return nil
}

// safety: extract beside the target and swap it in with a rename, so a
// concurrent reader sees the previous cache or the new one, never a partial
// one, and a rejected entry leaves the previous cache in place.
func extractIntoDirStaged(dir, stagePattern string, extract func(stage string) error) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, stagePattern)
	if err != nil {
		return err
	}
	if err := extract(stage); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}

	retired := stage + ".retired"
	swapped := false
	switch err := os.Rename(dir, retired); {
	case err == nil:
		swapped = true
	case !os.IsNotExist(err):
		_ = os.RemoveAll(stage)
		return err
	}
	if err := os.Rename(stage, dir); err != nil {
		if swapped {
			_ = os.Rename(retired, dir)
		}
		_ = os.RemoveAll(stage)
		return err
	}
	if swapped {
		_ = os.RemoveAll(retired)
	}
	return nil
}
