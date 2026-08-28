package boxslot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func PurgeIfIdle(lockDir string) (removed int, live []Holder, err error) {
	root, err := os.OpenRoot(lockDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	defer func() { _ = root.Close() }()
	return PurgeIfIdleInRoot(root, lockDir)
}

func PurgeIfIdleInRoot(root *os.Root, displayPath string) (removed int, live []Holder, err error) {
	return purgeIfIdleInRoot(root, displayPath, nil)
}

type purgeCandidate struct {
	name string
	info os.FileInfo
	file *os.File
}

var errCoordBusy = errors.New("legacy box-slot coordination is busy")

func purgeIfIdleInRoot(root *os.Root, displayPath string, whileCoordinated func()) (removed int, live []Holder, err error) {
	coord, coordInfo, err := openPurgeCoord(root, displayPath)
	if err != nil {
		return 0, nil, err
	}
	if err := flockExclusiveNonblock(coord); err != nil {
		_ = coord.Close()
		return 0, nil, fmt.Errorf("%w: %s", errCoordBusy, filepath.Join(displayPath, "coord.lock"))
	}
	currentCoord, err := root.Lstat("coord.lock")
	if err != nil || !os.SameFile(coordInfo, currentCoord) {
		_ = flockUnlock(coord)
		_ = coord.Close()
		if err != nil {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("boxslot: %s changed while locking", filepath.Join(displayPath, "coord.lock"))
	}
	defer func() { err = errors.Join(err, flockUnlock(coord), coord.Close()) }()
	if whileCoordinated != nil {
		whileCoordinated()
	}

	dir, err := root.Open(".")
	if err != nil {
		return 0, nil, err
	}
	entries, readErr := dir.ReadDir(-1)
	if err := dir.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if readErr != nil {
		return 0, nil, readErr
	}
	var candidates []purgeCandidate
	busy := false
	defer func() {
		for _, candidate := range candidates {
			err = errors.Join(err, flockUnlock(candidate.file), candidate.file.Close())
		}
	}()
	for _, e := range entries {
		if e.IsDir() || e.Name() == "coord.lock" {
			continue
		}
		info, err := root.Lstat(e.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, nil, err
		}
		if !info.Mode().IsRegular() {
			busy = true
			continue
		}
		f, err := root.OpenFile(e.Name(), os.O_RDWR, 0)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, nil, err
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			if statErr != nil {
				return 0, nil, statErr
			}
			return 0, nil, fmt.Errorf("boxslot: %s changed while opening", filepath.Join(displayPath, e.Name()))
		}
		if err := flockExclusiveNonblock(f); err != nil {
			_ = f.Close()
			busy = true
			if strings.HasPrefix(e.Name(), holderPrefix) {
				h := Holder{Path: filepath.Join(displayPath, e.Name()), Live: true}
				h.PID, h.ClaimedAt, _ = parseHolderName(e.Name())
				if b, readErr := root.ReadFile(e.Name()); readErr == nil {
					h.RunID = lastRunLine(b)
				}
				live = append(live, h)
			}
			continue
		}
		candidates = append(candidates, purgeCandidate{name: e.Name(), info: info, file: f})
	}
	if len(live) > 0 || busy {
		return 0, live, nil
	}
	for _, candidate := range candidates {
		current, err := root.Lstat(candidate.name)
		if err != nil || !os.SameFile(candidate.info, current) {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, nil, err
			}
			return 0, nil, fmt.Errorf("boxslot: %s changed before removal", filepath.Join(displayPath, candidate.name))
		}
	}
	for _, candidate := range candidates {
		if err := root.Remove(candidate.name); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, nil, err
		}
		removed++
	}
	return removed, nil, nil
}

func openPurgeCoord(root *os.Root, displayPath string) (*os.File, os.FileInfo, error) {
	for range 5 {
		info, err := root.Lstat("coord.lock")
		if errors.Is(err, os.ErrNotExist) {
			f, createErr := root.OpenFile("coord.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, nil, createErr
			}
			created, statErr := f.Stat()
			if statErr != nil {
				_ = f.Close()
				return nil, nil, statErr
			}
			return f, created, nil
		}
		if err != nil {
			return nil, nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("boxslot: %s is not a regular file", filepath.Join(displayPath, "coord.lock"))
		}
		f, openErr := root.OpenFile("coord.lock", os.O_RDWR, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return nil, nil, openErr
		}
		opened, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			if statErr != nil {
				return nil, nil, statErr
			}
			continue
		}
		return f, opened, nil
	}
	return nil, nil, fmt.Errorf("boxslot: %s kept changing while opening", filepath.Join(displayPath, "coord.lock"))
}

func probeHolderLive(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	return probeHolderLiveFile(f)
}

func probeHolderLiveFile(f *os.File) (bool, error) {
	if err := flockExclusiveNonblock(f); err != nil {
		return true, nil
	}
	_ = flockUnlock(f)
	return false, nil
}
