package boxslot

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const holderPrefix = "holder-"

type Holder struct {
	PID int

	ClaimedAt time.Time

	RunID string

	Path string

	Live bool
}

func Holders(lockDir string) ([]Holder, error) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var holders []Holder
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), holderPrefix) {
			continue
		}
		path := filepath.Join(lockDir, e.Name())
		h := Holder{Path: path}
		h.PID, h.ClaimedAt, _ = parseHolderName(e.Name())
		if b, err := os.ReadFile(path); err == nil {
			h.RunID = lastRunLine(b)
		}
		live, err := probeHolderLive(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		h.Live = live
		holders = append(holders, h)
	}
	sort.Slice(holders, func(i, j int) bool {
		if !holders[i].ClaimedAt.Equal(holders[j].ClaimedAt) {
			return holders[i].ClaimedAt.Before(holders[j].ClaimedAt)
		}
		return holders[i].Path < holders[j].Path
	})
	return holders, nil
}

func HoldersInRoot(root *os.Root, displayPath string) ([]Holder, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := dir.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if readErr != nil {
		return nil, readErr
	}
	var holders []Holder
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), holderPrefix) {
			continue
		}
		h := Holder{Path: filepath.Join(displayPath, e.Name())}
		h.PID, h.ClaimedAt, _ = parseHolderName(e.Name())
		if b, err := root.ReadFile(e.Name()); err == nil {
			h.RunID = lastRunLine(b)
		}
		f, err := root.OpenFile(e.Name(), os.O_RDWR, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		live, probeErr := probeHolderLiveFile(f)
		closeErr := f.Close()
		if probeErr != nil {
			return nil, probeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		h.Live = live
		holders = append(holders, h)
	}
	sort.Slice(holders, func(i, j int) bool {
		if !holders[i].ClaimedAt.Equal(holders[j].ClaimedAt) {
			return holders[i].ClaimedAt.Before(holders[j].ClaimedAt)
		}
		return holders[i].Path < holders[j].Path
	})
	return holders, nil
}

func lastRunLine(b []byte) string {
	run := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, found := strings.CutPrefix(line, "run="); found {
			run = strings.TrimSpace(v)
		}
	}
	return run
}

func parseHolderName(name string) (pid int, claimedAt time.Time, ok bool) {
	pid, nano, _, ok := parseMarkerName(name, holderPrefix)
	if !ok {
		return 0, time.Time{}, false
	}
	return pid, time.Unix(0, nano), true
}

func parseMarkerName(name, prefix string) (pid int, nano int64, seq uint64, ok bool) {
	body, found := strings.CutPrefix(name, prefix+"pid")
	if !found {
		return 0, 0, 0, false
	}
	body, found = strings.CutSuffix(body, ".lock")
	if !found {
		return 0, 0, 0, false
	}
	pidPart, rest, found := strings.Cut(body, "-")
	if !found {
		return 0, 0, 0, false
	}
	nanoPart, seqPart, found := strings.Cut(rest, "-")
	if !found {
		return 0, 0, 0, false
	}
	pid, err := strconv.Atoi(pidPart)
	if err != nil || pid <= 0 {
		return 0, 0, 0, false
	}
	nano, err = strconv.ParseInt(nanoPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	seq, err = strconv.ParseUint(seqPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return pid, nano, seq, true
}
