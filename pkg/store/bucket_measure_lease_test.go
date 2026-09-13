package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestRunBucketMeasureLeased_OneHolderPerWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	measured := 0
	ran, err := st.RunBucketMeasureLeased(ctx, "alpha", time.Hour, time.Minute, func(context.Context) error {
		measured++
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("first measurement ran=%t err=%v", ran, err)
	}

	ran, err = st.RunBucketMeasureLeased(ctx, "beta", time.Hour, time.Minute, func(context.Context) error {
		measured++
		return nil
	})
	if err != nil {
		t.Fatalf("second measurement: %v", err)
	}
	if ran {
		t.Error("a second replica measured inside the same window")
	}
	if measured != 1 {
		t.Errorf("the store was measured %d times in one window, want 1", measured)
	}
}

func TestRunBucketMeasureLeased_ReturnsTheMeasurementError(t *testing.T) {
	st := storetest.Open(t)
	want := errors.New("bucket unreachable")

	ran, err := st.RunBucketMeasureLeased(context.Background(), "alpha", time.Hour, time.Minute,
		func(context.Context) error { return want })
	if !ran {
		t.Error("a failed measurement reports that it did not run")
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to wrap %v", err, want)
	}
}

func TestRunBucketMeasureLeased_AFailedWindowIsClaimableAgain(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	if _, err := st.RunBucketMeasureLeased(ctx, "alpha", time.Hour, time.Minute,
		func(context.Context) error { return errors.New("listing failed") }); err == nil {
		t.Fatal("the failing measurement reported success")
	}

	ran, err := st.RunBucketMeasureLeased(ctx, "beta", time.Hour, time.Minute,
		func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("retry after a failed measurement: %v", err)
	}
	if !ran {
		t.Error("a window whose measurement failed stayed claimed, so the total never refreshes")
	}
}

func TestRunBucketMeasureLeased_RefusesAnEmptyMeasurement(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.RunBucketMeasureLeased(context.Background(), "alpha", time.Hour, time.Minute, nil); err == nil {
		t.Error("a nil measurement function was accepted")
	}
	if _, err := st.RunBucketMeasureLeased(context.Background(), "alpha", 0, time.Minute,
		func(context.Context) error { return nil }); err == nil {
		t.Error("a zero window was accepted")
	}
}
