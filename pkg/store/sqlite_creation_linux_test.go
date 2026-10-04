package store_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenSQLiteWithoutHardLinks(t *testing.T) {
	if os.Getenv("SPARKWING_STORE_NO_LINK_HELPER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenSQLiteWithoutHardLinks$")
		cmd.Env = append(os.Environ(), "SPARKWING_STORE_NO_LINK_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("no-link helper: %v\n%s", err, out)
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// safety: SQLite opens files on multiple Go threads, so capability denial
	// must cover every thread.
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_LINKAT, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EOPNOTSUPP)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	if errno != 0 {
		t.Fatal(errno)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := os.Link(path, path+"-link"); !errors.Is(err, unix.EOPNOTSUPP) {
		t.Fatalf("hard-link denial: %v", err)
	}
	if err := st.CreateRun(t.Context(), store.Run{ID: "no-links", Pipeline: "sample", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRun(t.Context(), "no-links"); err != nil {
		t.Fatal(err)
	}
}
