//go:build windows

package nodehost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/pkg/protofd"
)

func TestSDKProcessWindowsChild(t *testing.T) {
	if slices.Contains(os.Args, protofd.HandlesArg) {
		os.Exit(0)
	}
}

func FuzzSDKProcessClearsProtocolInheritance(f *testing.F) {
	f.Add(false, false, false)
	f.Add(true, true, false)
	f.Add(false, true, true)
	f.Add(true, false, true)
	f.Fuzz(func(t *testing.T, inheritRequests, inheritFrames, refused bool) {
		requests, requestWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer requests.Close()
		defer requestWriter.Close()
		frameReader, frames, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer frameReader.Close()
		defer frames.Close()
		for handle, inherited := range map[windows.Handle]bool{windows.Handle(requests.Fd()): inheritRequests, windows.Handle(frames.Fd()): inheritFrames} {
			var flags uint32
			if inherited {
				flags = windows.HANDLE_FLAG_INHERIT
			}
			if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, flags); err != nil {
				t.Fatal(err)
			}
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestSDKProcessWindowsChild$", "--")
		if refused {
			cmd.Path = filepath.Join(t.TempDir(), "missing.exe")
		}
		process, err := startSDKProcess(cmd, requests, frames)
		if process != nil {
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
				defer cancel()
				if err := process.Finish(ctx, 0); err != nil {
					t.Error(err)
				}
			})
		}
		if (err != nil) != refused {
			t.Fatalf("refused=%v error=%v", refused, err)
		}
		for _, handle := range []windows.Handle{windows.Handle(requests.Fd()), windows.Handle(frames.Fd())} {
			var flags uint32
			getFlags := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
			if ok, _, err := getFlags.Call(uintptr(handle), uintptr(unsafe.Pointer(&flags))); ok == 0 {
				t.Fatal(err)
			}
			if flags&windows.HANDLE_FLAG_INHERIT != 0 {
				t.Fatal("protocol handle remains inheritable in the host")
			}
		}
	})
}
