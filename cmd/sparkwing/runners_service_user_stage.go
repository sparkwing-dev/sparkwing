package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

// safety: the originals may sit in a home the account cannot enter, so a root-owned copy
// is what the account executes; it sits beside the account homes because some hosts
// mount /tmp noexec.
func stageServiceBinaries(goos, runnerBinary string) (self, runner string, cleanup func(), err error) {
	root := runnerservice.DefaultHomeRoot(goos)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", nil, err
	}
	dir, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove staged binaries %s: %v\n", dir, err)
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	if err = os.Chmod(dir, 0o755); err != nil {
		return "", "", nil, err
	}
	source, err := installsite.Self()
	if err != nil {
		return "", "", nil, err
	}
	self = filepath.Join(dir, "sparkwing")
	if err = copyExecutable(source, self); err != nil {
		return "", "", nil, err
	}
	if runnerBinary != "" {
		runner = filepath.Join(dir, runnerBinaryName)
		if err = copyExecutable(runnerBinary, runner); err != nil {
			return "", "", nil, err
		}
	}
	return self, runner, cleanup, nil
}

func copyExecutable(source, target string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Close()); err != nil {
		return err
	}
	return os.Chmod(target, 0o755)
}

func installRunnerBinary(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	staged := target + ".new"
	if err := os.Remove(staged); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := copyExecutable(source, staged); err != nil {
		return err
	}
	return os.Rename(staged, target)
}
