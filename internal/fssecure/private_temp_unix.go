//go:build !windows

package fssecure

import "os"

func mkdirPrivateTemp(parent, prefix string) (string, error) {
	directory, err := os.MkdirTemp(parent, prefix+"*")
	if err != nil {
		return "", err
	}
	if err := SecurePrivateDir(directory); err != nil {
		_ = os.Remove(directory)
		return "", err
	}
	return directory, nil
}
