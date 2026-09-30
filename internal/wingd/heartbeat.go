package wingd

import (
	"encoding/binary"
	"io"
	"os"
)

const heartbeatSize = 8

// ReadHeartbeat reads the daemon's progress counter from its heartbeat path.
func ReadHeartbeat(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var data [heartbeatSize]byte
	if _, err := f.ReadAt(data[:], 0); err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return binary.NativeEndian.Uint64(data[:]), nil
}
