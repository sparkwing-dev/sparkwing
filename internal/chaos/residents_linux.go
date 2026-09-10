//go:build linux

package chaos

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

func homeResidents(home string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	// safety: the harness names its home two ways -- an environment variable for
	// the CLI it hammers, and a --home flag for every actor and daemon it starts
	// -- so a check that reads only one of them cannot see what it launched.
	wantEnv := []byte("SPARKWING_HOME=" + home)
	wantArg := []byte(home)
	self := os.Getpid()
	var pids []int
	for _, proc := range entries {
		pid, convErr := strconv.Atoi(proc.Name())
		if convErr != nil || pid == self {
			continue
		}
		// safety: a process that exited between the listing and this read, and
		// another user's, are both unreadable and neither is ours to judge.
		env, envErr := os.ReadFile(filepath.Join("/proc", proc.Name(), "environ"))
		if envErr == nil && slices.ContainsFunc(bytes.Split(env, []byte{0}),
			func(e []byte) bool { return bytes.Equal(e, wantEnv) }) {
			pids = append(pids, pid)
			continue
		}
		args, argErr := os.ReadFile(filepath.Join("/proc", proc.Name(), "cmdline"))
		if argErr != nil {
			continue
		}
		fields := bytes.Split(args, []byte{0})
		for i, f := range fields {
			if bytes.Equal(f, []byte("--home")) && i+1 < len(fields) && bytes.Equal(fields[i+1], wantArg) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}
