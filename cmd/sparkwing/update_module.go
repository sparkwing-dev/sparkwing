package main

import (
	"errors"
	"io"
	"os"
)

var (
	errUpdateModuleType    = errors.New("SDK module must be a regular file")
	errUpdateModuleSize    = errors.New("SDK module exceeds the 1 MiB inspection limit")
	errUpdateModuleChanged = errors.New("SDK module changed during inspection")
)

func readUpdateModule(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errUpdateModuleType
	}
	if before.Size() > maxMetadataBytes {
		return nil, errUpdateModuleSize
	}
	file, err := openUpdateInput(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errUpdateModuleChanged
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMetadataBytes {
		return nil, errUpdateModuleSize
	}
	return data, nil
}
