package wingd

import (
	"encoding/binary"
	"os"
)

type heartbeatWriter struct {
	path string
}

func openHeartbeat(path string) (*heartbeatWriter, error) {
	if err := os.WriteFile(path, make([]byte, heartbeatSize), 0o600); err != nil {
		return nil, err
	}
	return &heartbeatWriter{path: path}, nil
}

func (h *heartbeatWriter) write(counter uint64) error {
	var data [heartbeatSize]byte
	binary.NativeEndian.PutUint64(data[:], counter)
	return os.WriteFile(h.path, data[:], 0o600)
}

func (h *heartbeatWriter) close() error { return nil }
