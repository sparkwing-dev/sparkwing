package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// RetainExecutable copies an executable into directory under its SHA-256, verifying acceptedBuild when supplied.
func RetainExecutable(directory, executable, acceptedBuild string) (_, _ string, result error) {
	dir := directory
	if err := fssecure.EnsureDir(dir); err != nil {
		return "", "", err
	}
	source, err := os.Open(executable)
	if err != nil {
		return "", "", err
	}
	defer func() { result = errors.Join(result, source.Close()) }()
	build := acceptedBuild
	if build == "" {
		digest := sha256.New()
		if _, err := io.Copy(digest, source); err != nil {
			return "", "", err
		}
		build = hex.EncodeToString(digest.Sum(nil))
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return "", "", err
		}
	}
	decoded, err := hex.DecodeString(build)
	if err != nil || len(decoded) != sha256.Size {
		return "", "", errors.New("invalid accepted local runner executable digest")
	}
	name := build
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if existing, err := os.Open(path); err == nil {
		digest := sha256.New()
		equal, compareErr := sameExecutableBytes(io.TeeReader(source, digest), existing)
		if err := errors.Join(compareErr, existing.Close()); err != nil {
			return "", "", err
		}
		if !equal || hex.EncodeToString(digest.Sum(nil)) != build {
			return "", "", errors.New("pinned local runner executable digest differs")
		}
		return path, build, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	file, err := os.CreateTemp(dir, ".runner-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, digest), source)
	if err := errors.Join(copyErr, file.Chmod(0o700), file.Close()); err != nil {
		return "", "", err
	}
	if hex.EncodeToString(digest.Sum(nil)) != build {
		return "", "", errors.New("local runner executable changed during preparation")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", "", err
	}
	return path, build, nil
}

// perf: comparing source and retained bytes lets the caller verify both with one digest and no staging write.
func sameExecutableBytes(source, existing io.Reader) (bool, error) {
	var a, b [32 * 1024]byte
	for {
		na, ea := io.ReadFull(source, a[:])
		nb, eb := io.ReadFull(existing, b[:])
		for _, err := range []error{ea, eb} {
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return false, err
			}
		}
		if !bytes.Equal(a[:na], b[:nb]) {
			return false, nil
		}
		if ea != nil || eb != nil {
			return errors.Is(ea, eb), nil
		}
	}
}
