package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxGoModuleBytes = 1 << 20

func readPinFile(path string) (body []byte, err error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("SDK input %s must be a regular file", filepath.Base(path))
	}
	bounded := filepath.Base(path) == "go.mod"
	if bounded && before.Size() > maxGoModuleBytes {
		return nil, errors.New("SDK module exceeds the 1 MiB inspection limit")
	}
	file, err := openPinFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("SDK input changed during inspection")
	}
	var reader io.Reader = file
	if bounded {
		reader = io.LimitReader(file, maxGoModuleBytes+1)
	}
	body, err = io.ReadAll(reader)
	if err == nil && bounded && len(body) > maxGoModuleBytes {
		err = errors.New("SDK module exceeds the 1 MiB inspection limit")
	}
	return body, err
}
