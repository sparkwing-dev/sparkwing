package orchestrator

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// safety: each node process leads its own session so its attempt is cleaned up alone, which puts it outside
// the trigger host's session; the host records each one as it starts, so a host killed before it reaps them
// leaves its claimer a list to reap instead of node processes running on with their capacity released.
func sessionRecorder(path string) func(pid int) {
	if path == "" {
		return nil
	}
	return func(pid int) {
		identity, err := procgroup.CaptureSession(pid)
		if err == nil {
			err = appendSessionRecord(path, identity)
		}
		if err != nil {
			slog.Default().Warn("record node process session", "pid", pid, "err", err)
		}
	}
}

func appendSessionRecord(path string, identity procgroup.SessionIdentity) error {
	line, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	f, err := fssecure.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// safety: a session whose leader's birth token no longer matches is another process reusing the pid, so
// KillSession leaves it alone; a session already empty is a node its host reaped.
func reapRecordedSessions(path string, logger *slog.Logger) {
	defer func() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove the node session record", "path", path, "err", err)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var identity procgroup.SessionIdentity
		if json.Unmarshal(scan.Bytes(), &identity) != nil {
			continue
		}
		if err := procgroup.KillSession(identity); err != nil {
			logger.Warn("reap a node process session the trigger host left", "session", identity.SessionID, "err", err)
		}
	}
}
