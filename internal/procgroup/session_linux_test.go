package procgroup

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestSessionIdentityVanished(t *testing.T) {
	for _, cause := range []error{syscall.ENOENT, syscall.ESRCH, syscall.EACCES} {
		t.Run(cause.Error(), func(t *testing.T) {
			_, _, err := sessionIdentityRead(123, func(string) ([]byte, error) {
				return nil, &os.PathError{Op: "open", Path: "/proc/123/stat", Err: cause}
			})
			if errors.Is(cause, syscall.EACCES) {
				if !errors.Is(err, cause) {
					t.Fatalf("inspection error = %v", err)
				}
			} else if !errors.Is(err, ErrProcessAbsent) {
				t.Fatalf("vanished process error = %v", err)
			}
		})
	}
}
