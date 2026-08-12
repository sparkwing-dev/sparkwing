//go:build !windows

package bincache

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const execLeaseEnv = "SPARKWING_INTERNAL_CACHE_LEASE"

var processExecLease struct {
	sync.Mutex
	file *os.File
}

func prepareExecLease(file *os.File, entry Entry, env []string) ([]string, func() error, error) {
	restore, err := cacheRetainAcrossExec(file)
	if err != nil {
		return nil, nil, err
	}
	coordinate := strconv.FormatUint(uint64(file.Fd()), 10) + ":" + entry.key + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(entry.root))
	return replaceExecLeaseEnv(env, coordinate), restore, nil
}

// AdoptExecLeaseFromEnv confines the lease inherited across the pipeline exec
// to this process. It restores close-on-exec before the runtime can spawn a
// daemon or node process and retains the descriptor until process exit.
func AdoptExecLeaseFromEnv() error {
	raw, ok := os.LookupEnv(execLeaseEnv)
	if !ok {
		return nil
	}
	if err := os.Unsetenv(execLeaseEnv); err != nil {
		return fmt.Errorf("clear inherited pipeline cache lease coordinate: %w", err)
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return errors.New("invalid inherited pipeline cache lease coordinate")
	}
	fd, err := strconv.ParseUint(parts[0], 10, 31)
	if err != nil || fd < 3 {
		return errors.New("invalid inherited pipeline cache lease descriptor")
	}
	root, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("invalid inherited pipeline cache lease root")
	}
	entry, err := pipelineEntryAt(string(root), parts[1])
	if err != nil {
		return fmt.Errorf("invalid inherited pipeline cache lease entry: %w", err)
	}
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(int(fd), &descriptorStat); err != nil {
		return fmt.Errorf("inspect inherited pipeline cache lease descriptor: %w", err)
	}
	var authorityStat unix.Stat_t
	if err := unix.Stat(entry.lockPath("lease"), &authorityStat); err != nil {
		return fmt.Errorf("inspect inherited pipeline cache lease authority: %w", err)
	}
	if descriptorStat.Dev != authorityStat.Dev || descriptorStat.Ino != authorityStat.Ino {
		return errors.New("inherited pipeline cache lease descriptor does not match its entry")
	}
	processExecLease.Lock()
	defer processExecLease.Unlock()
	if processExecLease.file != nil {
		return errors.New("pipeline cache lease was adopted more than once")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("read inherited pipeline cache lease flags: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("contain inherited pipeline cache lease: %w", err)
	}
	file := os.NewFile(uintptr(fd), "pipeline-cache-lease")
	if file == nil {
		return errors.New("inherited pipeline cache lease descriptor is unavailable")
	}
	processExecLease.file = file
	return nil
}

func replaceExecLeaseEnv(env []string, value string) []string {
	prefix := execLeaseEnv + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
