package wingd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func liveCPUStat(load float64) wingd.HostStat {
	return wingd.HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  16 << 30,
		LoadAverage:      load,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	}
}

func TestHeadroomSamplerLoopAdmitsAfterExternalCPUDrops(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(liveCPUStat(4.2))
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          sampler,
		GraceWindow:      -1,
		HeadroomFraction: -1,
		SampleInterval:   10 * time.Millisecond,
		HeadroomMaxAge:   120 * time.Millisecond,
		CapacityInterval: time.Hour,
		StallWindow:      time.Hour,
		StallInterval:    time.Hour,
	})

	holderClient := ensure(t, home, "")
	mustAcquire(t, holderClient, coreReq("cpu-holder", 0.1))
	cl := ensure(t, home, "")
	positions, result := acquireAsync(cl, wingwire.AdmissionRequest{
		RunID:    "cpu-recovery",
		SubLease: true,
		Resources: wingwire.HostResources{
			Cores: 3.8,
		},
	})
	select {
	case q := <-positions:
		if !strings.Contains(q.BlockingReason, "external load") {
			t.Fatalf("initial blocking reason = %q, want external CPU pressure", q.BlockingReason)
		}
	case r := <-result:
		t.Fatalf("run resolved under high external CPU: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("run neither queued nor resolved under high external CPU")
	}

	sampler.set(liveCPUStat(4.0))
	r := waitResult(t, result, 2*time.Second)
	if r.err != nil || r.lease == nil || r.lease.RunID != "cpu-recovery" {
		t.Fatalf("run after CPU recovery = lease=%v err=%v", r.lease, r.err)
	}
}

func TestHeadroomRefreshPushesUpdatedReasonToQueuedRun(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(liveCPUStat(4.2))
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          sampler,
		GraceWindow:      -1,
		HeadroomFraction: -1,
		SampleInterval:   10 * time.Millisecond,
		HeadroomMaxAge:   120 * time.Millisecond,
		CapacityInterval: time.Hour,
		StallWindow:      time.Hour,
		StallInterval:    time.Hour,
	})

	holderClient := ensure(t, home, "")
	mustAcquire(t, holderClient, coreReq("reason-holder", 0.1))
	cl := ensure(t, home, "")
	positions, result := acquireAsync(cl, wingwire.AdmissionRequest{
		RunID:    "reason-refresh",
		SubLease: true,
		Resources: wingwire.HostResources{
			Cores: 5,
		},
	})
	var initial wingwire.Queued
	select {
	case initial = <-positions:
		if initial.BlockingReason == "" {
			t.Fatal("initial queued update has no blocking reason")
		}
	case r := <-result:
		t.Fatalf("run resolved under high external CPU: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("run neither queued nor resolved under high external CPU")
	}

	sampler.set(liveCPUStat(3.8))
	deadline := time.After(2 * time.Second)
	for {
		select {
		case q := <-positions:
			if q.BlockingReason == initial.BlockingReason {
				continue
			}
			if q.Position != initial.Position || q.QueueLength != initial.QueueLength {
				t.Fatalf("headroom-only update changed queue position: before=%+v after=%+v", initial, q)
			}
			if !strings.Contains(q.BlockingReason, "available") || !strings.Contains(q.BlockingReason, "external load") {
				t.Fatalf("refreshed blocking reason = %q", q.BlockingReason)
			}
			select {
			case r := <-result:
				t.Fatalf("run resolved despite needing 5 cores: lease=%v err=%v", r.lease, r.err)
			default:
			}
			return
		case r := <-result:
			t.Fatalf("run resolved despite needing 5 cores: lease=%v err=%v", r.lease, r.err)
		case <-deadline:
			t.Fatalf("blocking reason stayed frozen at %q", initial.BlockingReason)
		}
	}
}
