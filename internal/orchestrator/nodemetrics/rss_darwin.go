//go:build darwin

package nodemetrics

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// processRSS returns the process's current resident set size, read from the
// process table.
//
// hack: releases build CGO_ENABLED=0, which puts task_info and
// proc_pid_rusage out of reach, and getrusage's ru_maxrss is a lifetime
// high-water mark -- every node that ran after the process peaked inherited
// that peak as its own footprint. ps reports the live figure, for the same
// reason wingd's darwin CPU sampler shells out to it.
func processRSS() (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, false
	}
	return parseProcessRSSKB(string(out))
}
