package fssecure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OpenPrivateCredential is OpenCredential for a private key: no other user may
// read it, and group read is admitted only for one of this process's groups.
func OpenPrivateCredential(directory, name string) (*os.File, error) {
	file, err := OpenCredential(directory, name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		err = verifyOwnerOnly(file, info)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("credential %s: %w", name, err)
	}
	return file, nil
}

// OpenCredential opens a regular, trusted-owner file confined to directory after resolving links.
func OpenCredential(directory, name string) (*os.File, error) {
	return openCredential(directory, name, false)
}

// OpenStateCredential is OpenCredential for state the controller writes on a
// kubelet-managed pod volume, such as the credential epoch: it may also be
// group-writable under one of this process's own groups (the pod fsGroup).
func OpenStateCredential(directory, name string) (*os.File, error) {
	return openCredential(directory, name, true)
}

func openCredential(directory, name string, ownGroupWrite bool) (*os.File, error) {
	if !filepath.IsLocal(name) {
		return nil, errors.New("credential name must stay within its directory")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	resolved, err := filepath.EvalSymlinks(filepath.Join(directory, name))
	if err != nil {
		if _, absent := root.Lstat(name); errors.Is(absent, os.ErrNotExist) {
			return nil, absent
		}
		return nil, fmt.Errorf("resolve credential %s: %v", name, err)
	}
	relative, err := filepath.Rel(directory, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, errors.New("credential resolves outside its directory")
	}
	info, err := root.Stat(relative)
	if err != nil {
		return nil, fmt.Errorf("inspect credential %s: %v", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential target must be a regular file")
	}
	file, err := openCredentialFile(root, relative)
	if err != nil {
		return nil, fmt.Errorf("open credential %s: %v", name, err)
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(info, opened)) {
		err = errors.New("credential changed while opening")
	}
	if err == nil {
		err = verifyCredentialFile(file, opened, ownGroupWrite)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("credential %s: %w", name, err)
	}
	return file, nil
}
