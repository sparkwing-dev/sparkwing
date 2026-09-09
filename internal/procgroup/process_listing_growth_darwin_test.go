//go:build darwin

package procgroup

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestProcessListingGrowthResamplesWithinCallerBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		finalErr error
	}{
		{name: "success"}, {name: "native failure", finalErr: syscall.EIO}, {name: "cancelled", finalErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := darwinProcessListing
			t.Cleanup(func() { darwinProcessListing = original })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			darwinProcessListing = func() ([]byte, error) {
				calls++
				if test.finalErr == context.Canceled {
					cancel()
					return nil, syscall.ENOMEM
				}
				if calls == 1 {
					return nil, syscall.ENOMEM
				}
				if test.finalErr != nil {
					return nil, test.finalErr
				}
				records, err := original()
				if err != nil {
					return nil, err
				}
				if len(records) < int(unsafe.Sizeof(unix.KinfoProc{})) {
					t.Fatal("native listing contains no complete record")
				}
				return records, nil
			}
			_ = ctx
			_, err := processTable(true)
			if test.finalErr == nil && err != nil {
				t.Fatalf("resized listing: %v", err)
			}
			if test.finalErr != nil && !errors.Is(err, test.finalErr) {
				t.Fatalf("listing error = %v, want %v", err, test.finalErr)
			}
			if test.finalErr == context.Canceled && !errors.Is(err, syscall.ENOMEM) {
				t.Fatalf("exhausted listing error = %v, want allocation cause", err)
			}
		})
	}
}
