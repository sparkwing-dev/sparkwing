package blob

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// LogClassDays parses the canonical r<days> class stored with a run's logs.
// Standing changes never alter the class of an existing object.
func LogClassDays(class string) (int, bool) {
	digits, ok := strings.CutPrefix(class, "r")
	if !ok {
		return 0, false
	}
	days, err := strconv.Atoi(digits)
	if err != nil || days < 1 || strconv.Itoa(days) != digits {
		return 0, false
	}
	return days, true
}

// ExpireLogs removes filesystem log objects whose stored class has elapsed.
// Call it from the controller's leased maintenance pass. CAS bytes and records
// have separate reachability rules and are never removed here. Identical PUT
// retries preserve the original file age, as S3 create-only writes do.
func (f *Filesystem) ExpireLogs(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := f.now()
	removed := 0
	err := fs.WalkDir(f.root.FS(), "logs", func(key string, entry fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if _, err := logObjectLimit(key); err != nil {
			return nil //nolint:nilerr // Other layouts and upload temporaries have no log retention class.
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		days, _ := LogClassDays(strings.Split(key, "/")[1])
		// safety: divide age before comparing days so a large operator class cannot overflow a duration.
		if now.Sub(info.ModTime())/(24*time.Hour) < time.Duration(days) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.root.Remove(key); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		removed++
		return nil
	})
	return removed, err
}
