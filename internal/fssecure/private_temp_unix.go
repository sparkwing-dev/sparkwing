//go:build !windows

package fssecure

import (
	"errors"
	"os"
)

func mkdirPrivateTemp(parent, prefix string) (string, error) {
	directory, err := os.MkdirTemp(parent, prefix+"*")
	if err != nil {
		return "", err
	}
	if err := SecurePrivateDir(directory); err != nil {
		return "", errors.Join(err, os.Remove(directory))
	}
	return directory, nil
}
