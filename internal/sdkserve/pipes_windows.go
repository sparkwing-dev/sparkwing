//go:build windows

package sdkserve

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func openPipes() (req, frames *os.File, err error) {
	raw := os.Getenv(ProtocolHandlesEnv)
	if err := os.Unsetenv(ProtocolHandlesEnv); err != nil {
		return nil, nil, err
	}
	reqRaw, frameRaw, ok := strings.Cut(raw, ",")
	if !ok {
		return nil, nil, fmt.Errorf("%s is %q, not <request>,<frame>; only a sparkwing host starts --sw-node-protocol", ProtocolHandlesEnv, raw)
	}
	var handles [2]syscall.Handle
	for i, s := range []string{reqRaw, frameRaw} {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%s handle %q: %w", ProtocolHandlesEnv, s, err)
		}
		handles[i] = syscall.Handle(n)
		// safety: the host sees end of frames only when every holder of the
		// frame handle is gone, so no command pipeline code starts may hold it.
		if err := syscall.SetHandleInformation(handles[i], syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
			return nil, nil, fmt.Errorf("protocol handle %d: %w", n, err)
		}
	}
	return os.NewFile(uintptr(handles[0]), "sw-requests"), os.NewFile(uintptr(handles[1]), "sw-frames"), nil
}
