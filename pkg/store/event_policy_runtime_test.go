package store

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestStoreDataEventsCapsCountKindAndUnchargedEvents(t *testing.T) {
	s, tenant, _, _ := resultFixture(t)
	if got, err := s.EventLimits(t.Context()); err != nil || got != DefaultEventLimits {
		t.Fatalf("default caps=%+v: %v", got, err)
	}
	if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", "k", make([]byte, DefaultEventLimits.MaxBytesPerEvent)); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("event kind escaped cap: %v", err)
	}
	if err := s.SetEventLimits(t.Context(), EventLimits{MaxBytesPerEvent: 5, MaxBytesPerRun: 10, MaxEventsPerRun: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AppendEventOnce(t.Context(), "run", "", "k", []byte("1234567")); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", "kkk", nil); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("uncharged bytes escaped run cap: %v", err)
	}
	if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", "kk", nil); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataEventsConcurrentCapsAndKindValidation(t *testing.T) {
	for _, caps := range []EventLimits{{MaxBytesPerEvent: 2, MaxBytesPerRun: 6}, {MaxEventsPerRun: 3}} {
		s, tenant, _, _ := resultFixture(t)
		if err := s.SetEventLimits(t.Context(), caps); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"", strings.Repeat("k", MaxEventKindBytes+1), "node started", "kind\n", "kind\x00"} {
			if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", kind, nil); !errors.Is(err, ErrInvalidEventKind) {
				t.Fatalf("invalid kind=%q: %v", kind, err)
			}
		}
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for range 8 {
			wg.Go(func() {
				_, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", "k", []byte("x"))
				results <- err
			})
		}
		wg.Wait()
		close(results)
		written := 0
		for err := range results {
			if err == nil {
				written++
			} else if !errors.Is(err, ErrStorageQuota) {
				t.Fatal(err)
			}
		}
		if written != 3 {
			t.Fatalf("concurrent appends admitted %d, want three", written)
		}
		if err := s.SetEventLimits(t.Context(), EventLimits{}); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{strings.Repeat("k", MaxEventKindBytes), "node_started", "sparkwing.step:log-1"} {
			if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", kind, nil); err != nil {
				t.Fatalf("valid kind=%q: %v", kind, err)
			}
		}
	}
}

func TestStoreDataEventsPreserveFundingAndFreeShare(t *testing.T) {
	s, tenant, _, _ := resultFixture(t)
	allowance := int64(1600)
	if _, err := s.SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageFreeAllowanceBytes: &allowance}); err != nil {
		t.Fatal(err)
	}
	appendBytes := func(size int) error {
		_, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "", "k", bytes.Repeat([]byte("x"), size-1))
		return err
	}
	if err := appendBytes(100); err != nil {
		t.Fatal(err)
	}
	if err := appendBytes(1); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("free share exceeded: %v", err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MicroCreditsPerCredit, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if err := appendBytes(500); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.HoldByOperator(t.Context(), "operator", "held", s.now()); err != nil {
		t.Fatal(err)
	}
	if err := appendBytes(1); !errors.Is(err, ErrStorageQuota) {
		t.Fatalf("held team retained funded share: %v", err)
	}
	if _, err := tenant.ReleaseOperatorHolds(t.Context(), "operator", "released", s.now()); err != nil {
		t.Fatal(err)
	}
	if err := appendBytes(1); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDataEventsPricedStorageRequiresCreditWithOrWithoutQuota(t *testing.T) {
	for _, quota := range []StorageQuota{{}, {MaxBytesPerRun: 100}} {
		s, tenant, _, _ := resultFixture(t)
		if err := tenant.SetStorageQuota(t.Context(), quota); err != nil {
			t.Fatal(err)
		}
		rate := int64(CloudStorageRateMicroPerGBDay)
		if _, err := s.SetCreditSettings(t.Context(), CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
			t.Fatal(err)
		}
		before := eventAccountingSnapshot(t, s)
		if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "job", "k", nil); !errors.Is(err, ErrInsufficientCredits) || eventAccountingSnapshot(t, s) != before {
			t.Fatalf("priced storage admitted without credit: %v", err)
		}
		if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MicroCreditsPerCredit, CreatedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.AppendEventCharged(t.Context(), "replica", "run", "job", "k", nil); err != nil {
			t.Fatal(err)
		}
	}
}
