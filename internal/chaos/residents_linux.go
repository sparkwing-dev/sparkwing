//go:build linux

package chaos

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
)

func homeResidents(home string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	want := []byte("SPARKWING_HOME=" + home)
	self := os.Getpid()
	var pids []int
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid == self {
			continue
		}
		// safety: another user's environ is unreadable and is not ours to judge.
		data, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if readErr != nil {
			continue
		}
		for _, entry := range bytes.Split(data, []byte{0}) {
			if bytes.Equal(entry, want) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}
