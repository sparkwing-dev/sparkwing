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
	if len(os.Args) != 4 || os.Args[2] != HandlesArg {
		return nil, nil, fmt.Errorf("%s requires %s <request>,<frame>", Arg, HandlesArg)
	}
	raw := os.Args[3]
	reqRaw, frameRaw, ok := strings.Cut(raw, ",")
	if !ok || reqRaw == "" || frameRaw == "" || strings.Contains(frameRaw, ",") {
		return nil, nil, fmt.Errorf("%s requires two decimal handles", HandlesArg)
	}
	var handles [2]syscall.Handle
	for i, s := range []string{reqRaw, frameRaw} {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%s handle: %w", HandlesArg, err)
		}
		if n == 0 || n > uint64(^uintptr(0)) {
			return nil, nil, fmt.Errorf("%s handle is outside the process handle range", HandlesArg)
		}
		handles[i] = syscall.Handle(n)
	}
	if handles[0] == handles[1] {
		return nil, nil, fmt.Errorf("%s must name separate request and frame handles", HandlesArg)
	}
	var clearErr error
	for _, h := range handles {
		if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, 0); err != nil && clearErr == nil {
			clearErr = err
		}
	}
	if clearErr != nil {
		return nil, nil, fmt.Errorf("clear protocol handle inheritance: %w", clearErr)
	}
	for _, h := range handles {
		kind, err := syscall.GetFileType(h)
		if err != nil || kind != syscall.FILE_TYPE_PIPE {
			return nil, nil, fmt.Errorf("protocol handle is not a pipe: %v", err)
		}
	}
	return os.NewFile(uintptr(handles[0]), "sw-requests"), os.NewFile(uintptr(handles[1]), "sw-frames"), nil
}
