// Package tarsafe extracts tar archives into a directory without letting an
// entry, a symlink or a decompression bomb reach outside it.
package tarsafe

import (
	"archive/tar"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// safety: bounds one extraction, so a gzip bomb cannot fill the disk.
var maxExtractBytes = int64(20 << 30)

// Policy tunes one extraction.
type Policy struct {
	// AllowSymlinks keeps symlink entries whose targets stay inside the
	// directory; without it every symlink entry is skipped.
	AllowSymlinks bool
	// MinDirPerm and MinFilePerm are OR-ed into every extracted mode.
	MinDirPerm  fs.FileMode
	MinFilePerm fs.FileMode
	// Rename maps an entry name to its extracted name; returning false
	// skips the entry.
	Rename func(name string) (string, bool)
}

func extractInRoot(tr *tar.Reader, dir string, policy Policy) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return ExtractIntoRoot(tr, root, policy)
}

// ExtractIntoRoot writes every entry of tr under an opened root, with the
// same refusals as extractInRoot.
func ExtractIntoRoot(tr *tar.Reader, root *os.Root, policy Policy) error {
	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var deferredDirModes []dirMode
	var links []string
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
		if policy.Rename != nil {
			kept := false
			if name, kept = policy.Rename(name); !kept {
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
			mode := hdr.FileInfo().Mode().Perm()&maxDirPerm | policy.MinDirPerm
			deferredDirModes = append(deferredDirModes, dirMode{rel, mode})

		case tar.TypeReg:
			if err := mkdirParent(root, rel); err != nil {
				return err
			}
			// safety: overwriting a read-only file from a previous
			// partial restore needs the remove first.
			_ = root.Remove(rel)
			f, err := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm()|policy.MinFilePerm)
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
			if !policy.AllowSymlinks {
				continue
			}
			if err := mkdirParent(root, rel); err != nil {
				return err
			}
			if err := symlinkStaysInside(root, rel, hdr.Linkname); err != nil {
				return err
			}
			_ = root.Remove(rel)
			if err := root.Symlink(filepath.FromSlash(hdr.Linkname), rel); err != nil {
				return err
			}
			links = append(links, rel)

		default:
		}
	}

	// safety: a later link can redirect an earlier one, so containment holds
	// only once every link exists.
	for _, rel := range links {
		if err := resolvesInside(root, rel); err != nil {
			return err
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
	if path.IsAbs(name) || filepath.VolumeName(name) != "" || strings.HasPrefix(name, string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
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
	if link == "" || path.IsAbs(linkname) || filepath.VolumeName(link) != "" || strings.HasPrefix(link, string(filepath.Separator)) {
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

const maxLinkHops = 255

// safety: a missing component resolves lexically, since a later writer could
// create it as a directory that the link then climbs out of.
func resolvesInside(root *os.Root, rel string) error {
	pending := strings.Split(rel, string(filepath.Separator))
	var resolved []string
	for hops := 0; len(pending) > 0; {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return fmt.Errorf("archive symlink %q resolves outside the target directory", filepath.ToSlash(rel))
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		at := filepath.Join(append(resolved, part)...)
		fi, err := root.Lstat(at)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			resolved = append(resolved, part)
			continue
		}
		if hops++; hops > maxLinkHops {
			return fmt.Errorf("archive symlink %q has too many levels of links", filepath.ToSlash(rel))
		}
		target, err := root.Readlink(at)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
			return fmt.Errorf("archive symlink %q resolves outside the target directory", filepath.ToSlash(rel))
		}
		pending = append(strings.Split(target, string(filepath.Separator)), pending...)
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

// ExtractIntoRootStaged runs extract into a fresh directory beside name inside
// parent and renames it over name, so a concurrent reader sees the previous
// contents or the new ones, never a partial tree, and a failed extraction leaves
// the previous contents in place. Every step goes through parent, so a symlink
// on the way cannot move the restore outside it.
func ExtractIntoRootStaged(parent *os.Root, name, stagePrefix string, extract func(stage *os.Root) error) error {
	if !filepath.IsLocal(name) {
		return fmt.Errorf("restore target %q leaves its root", name)
	}
	dir := filepath.Dir(name)
	if err := parent.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	stage := filepath.Join(dir, stagePrefix+hex.EncodeToString(suffix[:]))
	if err := parent.Mkdir(stage, 0o700); err != nil {
		return err
	}
	stageRoot, err := parent.OpenRoot(stage)
	if err != nil {
		_ = parent.RemoveAll(stage)
		return err
	}
	err = extract(stageRoot)
	err = errors.Join(err, stageRoot.Close())
	if err != nil {
		_ = parent.RemoveAll(stage)
		return err
	}
	retired := stage + ".retired"
	swapped := false
	switch err := parent.Rename(name, retired); {
	case err == nil:
		swapped = true
	case !os.IsNotExist(err):
		_ = parent.RemoveAll(stage)
		return err
	}
	if err := parent.Rename(stage, name); err != nil {
		if swapped {
			_ = parent.Rename(retired, name)
		}
		_ = parent.RemoveAll(stage)
		return err
	}
	if swapped {
		_ = parent.RemoveAll(retired)
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}
