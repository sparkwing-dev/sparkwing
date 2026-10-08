//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package secrets

import (
	"encoding/base64"
	"io"
	"os"
	"strconv"
	"syscall"
	"testing"
)

func TestStopSharingRegisteredClosesTheHostsDescriptor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fd, err := syscall.Dup(int(w.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	t.Setenv(MaskValuesFDEnv, strconv.Itoa(fd))
	ShareRegisteredFromEnv()
	shareRegistered("late-value")
	StopSharingRegistered()
	shareRegistered("after-stop")
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.StdEncoding.EncodeToString([]byte("late-value")) + "\n"; string(got) != want {
		t.Fatalf("host read %q, want %q and then the end of the channel", got, want)
	}
}
