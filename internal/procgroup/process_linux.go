//go:build linux

package procgroup

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

var procRoot = "/proc"

// hack: these count from the state field rather than carrying the kernel's own
// field numbers, because the parenthesised command is split off before the
// numeric fields are indexed.
const (
	procStatState     = 0
	procStatGroup     = 2
	procStatSession   = 3
	procStatStart     = 19
	procStatMinFields = procStatStart + 1
)

var (
	nativeFallbackOnce sync.Once
	nativeFallbackLog  = func(err error) {
		slog.Warn("/proc process listing unavailable; falling back to a ps fork per listing", "err", err)
	}
)

func reportNativeFallback(err error) {
	nativeFallbackOnce.Do(func() { nativeFallbackLog(err) })
}

func nativeProcessTable(withSessions bool) ([]Info, bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		reportNativeFallback(err)
		return nil, false
	}
	processes := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "stat"))
		if err != nil {
			// safety: a process that exits mid-scan leaves a gap in the snapshot
			// rather than a listing this process should abandon.
			continue
		}
		process, err := parseProcStat(string(stat), withSessions)
		if err != nil {
			continue
		}
		processes = append(processes, process)
	}
	return processes, true
}

// hack: the command field is parenthesised and may itself hold spaces and
// parentheses, so the numeric fields are found from the last ')' rather than by
// splitting the whole line.
func parseProcStat(line string, withSession bool) (Info, error) {
	lparen := strings.IndexByte(line, '(')
	rparen := strings.LastIndexByte(line, ')')
	if lparen < 1 || rparen < lparen || rparen+2 >= len(line) {
		return Info{}, fmt.Errorf("malformed process stat %q", line)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:lparen]))
	if err != nil {
		return Info{}, fmt.Errorf("process stat pid: %w", err)
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < procStatMinFields {
		return Info{}, fmt.Errorf("short process stat for %d", pid)
	}
	group, err := strconv.Atoi(fields[procStatGroup])
	if err != nil {
		return Info{}, fmt.Errorf("process %d group: %w", pid, err)
	}
	start, err := strconv.ParseUint(fields[procStatStart], 10, 64)
	if err != nil {
		return Info{}, fmt.Errorf("process %d start time: %w", pid, err)
	}
	process := Info{
		PID:   pid,
		Group: group,
		State: fields[procStatState],
		Birth: strconv.FormatUint(start, 10),
	}
	if withSession {
		sid, err := strconv.Atoi(fields[procStatSession])
		if err != nil {
			return Info{}, fmt.Errorf("process %d session: %w", pid, err)
		}
		process.Session = sid
	}
	return process, nil
}
