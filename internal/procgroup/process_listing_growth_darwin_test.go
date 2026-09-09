//go:build darwin

package procgroup

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
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
				if errors.Is(test.finalErr, context.Canceled) {
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
			_, err := processTable(ctx, true)
			if test.finalErr == nil && err != nil {
				t.Fatalf("resized listing: %v", err)
			}
			if test.finalErr != nil && !errors.Is(err, test.finalErr) {
				t.Fatalf("listing error = %v, want %v", err, test.finalErr)
			}
			if errors.Is(test.finalErr, context.Canceled) && !errors.Is(err, syscall.ENOMEM) {
				t.Fatalf("exhausted listing error = %v, want allocation cause", err)
			}
		})
	}
}

func TestProcessListingGrowthStopsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := darwinProcessListing
		t.Cleanup(func() { darwinProcessListing = original })
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		calls := 0
		darwinProcessListing = func() ([]byte, error) {
			calls++
			time.Sleep(10 * time.Millisecond)
			synctest.Wait()
			return nil, syscall.ENOMEM
		}
		started := time.Now()
		_, err := processTable(ctx, false)
		if !errors.Is(err, syscall.ENOMEM) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("exhausted listing error = %v, want allocation and deadline causes", err)
		}
		if elapsed := time.Since(started); elapsed != 100*time.Millisecond {
			t.Fatalf("listing elapsed = %s, want caller deadline", elapsed)
		}
		if calls < 2 {
			t.Fatalf("listing calls = %d, want repeated sizing", calls)
		}
	})
}
