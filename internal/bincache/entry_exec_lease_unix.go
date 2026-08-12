//go:build !windows

package bincache

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	proofDir := filepath.Join(entry.root, "locks")
	if err := os.MkdirAll(proofDir, 0o700); err != nil {
		_ = restore()
		return nil, nil, err
	}
	proof, err := os.CreateTemp(proofDir, ".exec-proof-")
	if err != nil {
		_ = restore()
		return nil, nil, err
	}
	proofPath := proof.Name()
	cleanupProof := func() error {
		return errors.Join(cacheUnlock(proof), proof.Close(), os.Remove(proofPath))
	}
	if acquired, lockErr := cacheLock(proof, cacheLockExclusiveNonblock); lockErr != nil || !acquired {
		_ = cleanupProof()
		_ = restore()
		if lockErr != nil {
			return nil, nil, lockErr
		}
		return nil, nil, errors.New("pipeline cache exec proof is busy")
	}
	restoreProof, err := cacheRetainAcrossExec(proof)
	if err != nil {
		_ = cleanupProof()
		_ = restore()
		return nil, nil, err
	}
	coordinate := strconv.FormatUint(uint64(file.Fd()), 10) + ":" + entry.key + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(entry.root)) + ":" +
		strconv.FormatUint(uint64(proof.Fd()), 10) + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(proofPath))
	return replaceExecLeaseEnv(env, coordinate), func() error {
		return errors.Join(restore(), restoreProof(), cleanupProof())
	}, nil
}

// AdoptExecLeaseFromEnv confines the lease inherited across the pipeline exec
// to this process. It restores close-on-exec before the runtime can spawn a
// daemon or node process and retains the descriptor until process exit.
func AdoptExecLeaseFromEnv() (err error) {
	raw, ok := os.LookupEnv(execLeaseEnv)
	if !ok {
		return nil
	}
	if err := os.Unsetenv(execLeaseEnv); err != nil {
		return fmt.Errorf("clear inherited pipeline cache lease coordinate: %w", err)
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 5 {
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
	proofFD, err := strconv.ParseUint(parts[3], 10, 31)
	if err != nil || proofFD < 3 || proofFD == fd {
		return errors.New("invalid inherited pipeline cache lease proof descriptor")
	}
	proofPathBytes, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return errors.New("invalid inherited pipeline cache lease proof path")
	}
	proofPath := string(proofPathBytes)
	if filepath.Dir(proofPath) != filepath.Join(entry.root, "locks") || !strings.HasPrefix(filepath.Base(proofPath), ".exec-proof-") {
		return errors.New("invalid inherited pipeline cache lease proof path")
	}
	proof := os.NewFile(uintptr(proofFD), "pipeline-cache-lease-proof")
	if proof == nil {
		return errors.New("inherited pipeline cache lease proof descriptor is unavailable")
	}
	proofOwned := false
	defer func() {
		if !proofOwned {
			err = errors.Join(err, cacheUnlock(proof), proof.Close(), os.Remove(proofPath))
		}
	}()
	proofInfo, err := proof.Stat()
	if err != nil {
		return fmt.Errorf("inspect inherited pipeline cache lease proof descriptor: %w", err)
	}
	proofAuthority, err := os.Stat(proofPath)
	if err != nil {
		return fmt.Errorf("inspect inherited pipeline cache lease proof authority: %w", err)
	}
	if !os.SameFile(proofInfo, proofAuthority) {
		return errors.New("inherited pipeline cache lease proof descriptor does not match its authority")
	}
	probe, err := os.Open(proofPath)
	if err != nil {
		return fmt.Errorf("open inherited pipeline cache lease proof probe: %w", err)
	}
	probeAcquired, probeErr := cacheLock(probe, cacheLockExclusiveNonblock)
	if probeAcquired {
		_ = cacheUnlock(probe)
	}
	_ = probe.Close()
	if probeErr != nil {
		return fmt.Errorf("probe inherited pipeline cache lease proof: %w", probeErr)
	}
	if probeAcquired {
		return errors.New("inherited pipeline cache lease proof was not retained across exec")
	}
	if acquired, lockErr := cacheLock(proof, cacheLockExclusiveNonblock); lockErr != nil || !acquired {
		if lockErr != nil {
			return fmt.Errorf("verify inherited pipeline cache lease proof: %w", lockErr)
		}
		return errors.New("inherited pipeline cache lease proof is not owned by its descriptor")
	}
	leaseFile := os.NewFile(uintptr(fd), "pipeline-cache-lease")
	if leaseFile == nil {
		return errors.New("inherited pipeline cache lease descriptor is unavailable")
	}
	leaseOwned := false
	defer func() {
		if !leaseOwned {
			err = errors.Join(err, cacheUnlock(leaseFile), leaseFile.Close())
		}
	}()
	if _, err := cacheLock(leaseFile, cacheLockShared); err != nil {
		return fmt.Errorf("establish inherited pipeline cache lease authority: %w", err)
	}
	proofOwned = true
	if err := errors.Join(cacheUnlock(proof), proof.Close(), os.Remove(proofPath)); err != nil {
		return fmt.Errorf("retire inherited pipeline cache lease proof: %w", err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("read inherited pipeline cache lease flags: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("contain inherited pipeline cache lease: %w", err)
	}
	leaseOwned = true
	processExecLease.file = leaseFile
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
