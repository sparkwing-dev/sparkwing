//go:build linux

package wingd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const cgroupRoot = "/sys/fs/cgroup"

const cgroupSupported = true

func newCgroupLimiter(homeDir string, cores float64, mem uint64) (*cgroupLimiter, error) {
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("cgroup v2 not available at %s: %w", cgroupRoot, err)
	}
	sum := sha256.Sum256([]byte(homeDir))
	name := "sparkwing-" + hex.EncodeToString(sum[:])[:12]
	path := filepath.Join(cgroupRoot, name)
	if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create cgroup %s: %w", path, err)
	}
	_ = os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+cpu +memory"), 0)
	if err := os.WriteFile(filepath.Join(path, "cpu.max"), []byte(cpuMaxLine(cores)), 0); err != nil {
		return nil, fmt.Errorf("write cpu.max: %w", err)
	}
	if err := os.WriteFile(filepath.Join(path, "memory.max"), []byte(memMaxLine(mem)), 0); err != nil {
		return nil, fmt.Errorf("write memory.max: %w", err)
	}
	return &cgroupLimiter{path: path}, nil
}

func (c *cgroupLimiter) join(pid int) error {
	return os.WriteFile(filepath.Join(c.path, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0)
}
