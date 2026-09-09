package sparkwing

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SAFETY: Bounds one extraction, so a gzip bomb cannot fill the disk.
var maxExtractBytes = int64(20 << 30)

type tarExtractPolicy struct {
	allowSymlinks bool
	minDirPerm    fs.FileMode
	minFilePerm   fs.FileMode
	rename        func(name string) (string, bool)
}

func extractTarInRoot(archiveReader *tar.Reader, directory string, policy tarExtractPolicy) (resultErr error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()

	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var deferredDirModes []dirMode
	var written int64

	for {
		header, err := archiveReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}

		name := header.Name
		if policy.rename != nil {
			kept := false
			if name, kept = policy.rename(name); !kept {
				continue
			}
		}
		relative, err := secureArchiveRel(name)
		if err != nil {
			return err
		}
		if relative == "." {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			// SAFETY: A read-only directory mode would block extracting
			// its children; create writable, restore the mode afterwards.
			if err := root.MkdirAll(relative, provisionalDirPerm); err != nil {
				return err
			}
			// SAFETY: Chmod ignores the umask, so clamp the archive's mode
			// instead of letting it leave a world-writable directory.
			mode := header.FileInfo().Mode().Perm()&maxDirPerm | policy.minDirPerm
			deferredDirModes = append(deferredDirModes, dirMode{relative, mode})

		case tar.TypeReg:
			if err := mkdirParent(root, relative); err != nil {
				return err
			}
			// SAFETY: Overwriting a read-only file from a previous
			// partial restore needs the remove first.
			if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			file, err := root.OpenFile(relative, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode().Perm()|policy.minFilePerm)
			if err != nil {
				return err
			}
			bytesRead, err := io.Copy(file, io.LimitReader(archiveReader, maxExtractBytes-written+1))
			written += bytesRead
			if err != nil {
				return errors.Join(err, file.Close())
			}
			if written > maxExtractBytes {
				return errors.Join(fmt.Errorf("archive exceeds the %s extraction limit; refusing to fill the disk", humanBytes(maxExtractBytes)), file.Close())
			}
			if err := file.Close(); err != nil {
				return err
			}

		case tar.TypeSymlink:
			if !policy.allowSymlinks {
				continue
			}
			if err := mkdirParent(root, relative); err != nil {
				return err
			}
			if err := symlinkStaysInside(root, relative, header.Linkname); err != nil {
				return err
			}
			if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := root.Symlink(header.Linkname, relative); err != nil {
				return err
			}

		default:
		}
	}

	// SAFETY: Deepest-first restores nested read-only directories
	// without locking out their own just-extracted contents.
	for index := len(deferredDirModes) - 1; index >= 0; index-- {
		directoryMode := deferredDirModes[index]
		if err := root.Chmod(directoryMode.path, directoryMode.mode); err != nil {
			return err
		}
	}
	return nil
}

const (
	provisionalDirPerm = fs.FileMode(0o755)
	maxDirPerm         = fs.FileMode(0o755)
)

func mkdirParent(root *os.Root, relative string) error {
	parent := filepath.Dir(relative)
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

// SAFETY: os.Root constrains where a link is created, never where it points,
// so measure the target from the entry's real parent, not its lexical one.
func symlinkStaysInside(root *os.Root, relative, linkname string) error {
	link := filepath.FromSlash(linkname)
	if link == "" || filepath.IsAbs(link) {
		return fmt.Errorf("archive symlink %q -> %q is not a relative link", relative, linkname)
	}
	if err := refuseSymlinkedAncestors(root, relative); err != nil {
		return err
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(relative), link))
	if target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
		return fmt.Errorf("archive symlink %q -> %q escapes the target directory", relative, linkname)
	}
	return nil
}

// SAFETY: A symlinked ancestor makes the entry's real depth shallower than its
// name, which is how a link that reads as contained lands outside the root.
func refuseSymlinkedAncestors(root *os.Root, relative string) error {
	parent := filepath.Dir(relative)
	if parent == "." {
		return nil
	}
	parts := strings.Split(parent, string(filepath.Separator))
	for index := range parts {
		ancestor := filepath.Join(parts[:index+1]...)
		info, err := root.Lstat(ancestor)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("archive entry %q sits under the symlinked directory %q", relative, filepath.ToSlash(ancestor))
		}
	}
	return nil
}

// SAFETY: Staging preserves the previous cache when archive extraction fails.
func extractIntoDirStaged(directory, stagePattern string, extract func(stage string) error) error {
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, stagePattern)
	if err != nil {
		return err
	}
	if err := extract(stage); err != nil {
		return errors.Join(err, os.RemoveAll(stage))
	}

	retired := stage + ".retired"
	swapped := false
	switch err := os.Rename(directory, retired); {
	case err == nil:
		swapped = true
	case !os.IsNotExist(err):
		return errors.Join(err, os.RemoveAll(stage))
	}
	if err := os.Rename(stage, directory); err != nil {
		if swapped {
			err = errors.Join(err, os.Rename(retired, directory))
		}
		return errors.Join(err, os.RemoveAll(stage))
	}
	if swapped {
		return os.RemoveAll(retired)
	}
	return nil
}
