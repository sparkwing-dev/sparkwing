package toolchain

import (
	"errors"
	"io"
	"os"
)

const maxHoldBytes = 4096

func readHoldFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("hold configuration must be a regular file")
	}
	if before.Size() > maxHoldBytes {
		return nil, errors.New("hold configuration exceeds 4 KiB")
	}
	file, err := openHoldFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("hold configuration changed during inspection")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxHoldBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxHoldBytes {
		return nil, errors.New("hold configuration exceeds 4 KiB")
	}
	return body, nil
}
