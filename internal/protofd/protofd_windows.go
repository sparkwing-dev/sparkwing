//go:build windows

package protofd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func open() (*os.File, *os.File, error) {
	raw := os.Getenv(HandlesEnv)
	reqRaw, frameRaw, ok := strings.Cut(raw, ",")
	if !ok {
		return nil, nil, fmt.Errorf("%s is %q, not <request>,<frame>; only a sparkwing host starts %s", HandlesEnv, raw, Arg)
	}
	var handles [2]syscall.Handle
	for i, s := range []string{reqRaw, frameRaw} {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%s handle %q: %w", HandlesEnv, s, err)
		}
		handles[i] = syscall.Handle(n)
	}
	for _, h := range handles {
		if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
			return nil, nil, fmt.Errorf("protocol handle %d: %w", h, err)
		}
	}
	return os.NewFile(uintptr(handles[0]), "sw-requests"), os.NewFile(uintptr(handles[1]), "sw-frames"), nil
}
