//go:build !windows

package fssecure

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const auditSupported = true

func tighten(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

func tightenOpen(file *os.File, mode fs.FileMode) error { return file.Chmod(mode) }

func repairTree(root string, expected os.FileInfo, dryRun bool) ([]Change, error) {
	clean := filepath.Clean(root)
	if clean == "." || filepath.Dir(clean) == clean {
		return nil, fmt.Errorf("refuse permission repair for unsafe root %q", root)
	}
	rootInfo, err := os.Lstat(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refuse permission repair through symlink root %q", root)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("permission repair root %q is not a directory", root)
	}
	if expected == nil {
		return nil, fmt.Errorf("permission repair root %q has no recognized identity", root)
	}
	if !os.SameFile(expected, rootInfo) {
		return nil, fmt.Errorf("permission repair root %q changed after recognition", root)
	}
	absRoot, err := filepath.Abs(clean)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(resolvedRoot) == resolvedRoot {
		return nil, fmt.Errorf("refuse permission repair for unsafe root %q", root)
	}
	if userHome, homeErr := os.UserHomeDir(); homeErr == nil {
		absHome, absErr := filepath.Abs(userHome)
		resolvedHome, resolveErr := filepath.EvalSymlinks(absHome)
		if absErr == nil && resolveErr == nil {
			rel, relErr := filepath.Rel(resolvedRoot, resolvedHome)
			if relErr == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
				return nil, fmt.Errorf("refuse permission repair for root %q containing the user home", root)
			}
		}
	}
	var beforeOpen unix.Stat_t
	if err := unix.Stat(resolvedRoot, &beforeOpen); err != nil {
		return nil, err
	}
	fd, err := unix.Open(resolvedRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	rootFile := os.NewFile(uintptr(fd), resolvedRoot)
	if rootFile == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open permission repair root %q", root)
	}
	defer func() { _ = rootFile.Close() }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, err
	}
	if !sameFile(beforeOpen, opened) {
		return nil, fmt.Errorf("permission repair root %q changed while it was inspected", root)
	}
	openedInfo, err := rootFile.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expected, openedInfo) {
		return nil, fmt.Errorf("permission repair root %q changed after recognition", root)
	}
	return repairOpenedTree(rootFile, opened, ".", dryRun)
}

func repairOpenedTree(dir *os.File, stat unix.Stat_t, rel string, dryRun bool) ([]Change, error) {
	changes, err := repairOpenedMode(dir, stat, rel, DirMode, dryRun)
	if err != nil {
		return nil, err
	}
	for {
		names, readErr := dir.Readdirnames(128)
		for _, name := range names {
			childChanges, err := repairOpenedChild(int(dir.Fd()), name, filepath.Join(rel, name), dryRun)
			changes = append(changes, childChanges...)
			if err != nil {
				return changes, err
			}
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, os.ErrNotExist) {
			return changes, nil
		}
		if readErr != nil {
			return changes, readErr
		}
	}
}

func repairOpenedChild(parentFD int, name, rel string, dryRun bool) ([]Change, error) {
	var beforeOpen unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &beforeOpen, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	kind := beforeOpen.Mode & unix.S_IFMT
	if kind != unix.S_IFDIR && kind != unix.S_IFREG {
		return nil, nil
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if kind == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parentFD, name, flags, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s without following links: %w", rel, err)
	}
	openedFile := os.NewFile(uintptr(fd), rel)
	if openedFile == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open permission repair path %q", rel)
	}
	defer func() { _ = openedFile.Close() }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, err
	}
	if !sameFile(beforeOpen, opened) {
		return nil, fmt.Errorf("permission repair path %q changed while it was inspected", rel)
	}
	if kind == unix.S_IFDIR {
		return repairOpenedTree(openedFile, opened, rel, dryRun)
	}
	target := FileMode
	if fs.FileMode(opened.Mode).Perm()&0o111 != 0 && preserveExecutable(rel) {
		target = DirMode
	}
	return repairOpenedMode(openedFile, opened, rel, target, dryRun)
}

func repairOpenedMode(file *os.File, stat unix.Stat_t, rel string, allowed fs.FileMode, dryRun bool) ([]Change, error) {
	before := fs.FileMode(stat.Mode).Perm()
	target := before & allowed
	hasSpecial := stat.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0
	if before == target && !hasSpecial {
		return nil, nil
	}
	if !dryRun {
		if err := unix.Fchmod(int(file.Fd()), uint32(target.Perm())); err != nil {
			return nil, err
		}
	}
	return []Change{{
		Path:   rel,
		Before: fmt.Sprintf("%04o", before),
		After:  fmt.Sprintf("%04o", target),
	}}, nil
}

func sameFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

func preserveExecutable(rel string) bool {
	slash := filepath.ToSlash(rel)
	if strings.HasPrefix(slash, "node-runner/") || strings.HasPrefix(slash, "trigger-loop/") {
		return true
	}
	// safety: the store holds CLI releases a pinned repo execs; 0600 breaks every such repo.
	if strings.HasPrefix(slash, "toolchains/") {
		return true
	}
	if !strings.HasPrefix(slash, "cache/pipelines/") {
		return false
	}
	base := filepath.Base(rel)
	return base == "pipelines" || base == "pipelines.exe"
}
