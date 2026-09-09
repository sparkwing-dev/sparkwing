//go:build darwin

package wingd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestDarwinFreeMemory_PressureIsNotMeasurement(t *testing.T) {
	free, measured := darwinFreeMemory(16<<30, "62")
	if measured || free != 0 {
		t.Fatalf("pressure level reported %d available bytes, measured=%v", free, measured)
	}
}

func TestDarwinFreeMemory_VMCounters(t *testing.T) {
	for _, pageSize := range []uint64{4096, 16384} {
		t.Run(fmt.Sprint(pageSize), func(t *testing.T) {
			output := fmt.Sprintf("Mach Virtual Memory Statistics: (page size of %d bytes)\n"+
				"Pages free: 4096.\nPages inactive: 245760.\n"+
				"Pages purgeable: 32768.\nPages speculative: 8192.\n"+
				"Pages occupied by compressor: 524288.\n", pageSize)
			free, measured := darwinFreeMemory(16<<30, output)
			want := uint64(4096+245760) * pageSize
			if !measured || free != want {
				t.Fatalf("free=%d, measured=%v; want %d measured bytes", free, measured, want)
			}
			stat := HostStat{TotalMemoryBytes: 16 << 30, FreeMemoryBytes: free, MemoryMeasured: measured}
			reserve, external := memReserveAndExternal(stat, 0, DefaultHeadroomFraction)
			available := headroomFromReserveExternal(stat.TotalMemoryBytes, reserve, external)
			var wantAvailable uint64
			if free > reserve {
				wantAvailable = free - reserve
			}
			if available != wantAvailable {
				t.Fatalf("admission granted %d bytes with %d free and %d reserved", available, free, reserve)
			}
		})
	}
}

func TestDarwinFreeMemory_InvalidCounters(t *testing.T) {
	const valid = "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\nPages inactive: 20.\n"
	for name, output := range map[string]string{
		"empty":             "",
		"missing header":    "Pages free: 10.\nPages inactive: 20.\n",
		"missing free":      strings.ReplaceAll(valid, "Pages free: 10.\n", ""),
		"missing inactive":  strings.ReplaceAll(valid, "Pages inactive: 20.\n", ""),
		"truncated count":   strings.ReplaceAll(valid, "20.", "2"),
		"duplicate":         valid + "Pages free: 10.\n",
		"negative":          strings.ReplaceAll(valid, "10.", "-10."),
		"malformed":         strings.ReplaceAll(valid, "10.", "unknown"),
		"overflow":          strings.ReplaceAll(valid, "10.", "18446744073709551615."),
		"exceeds RAM":       strings.ReplaceAll(valid, "10.", "1048576."),
		"zero page size":    strings.ReplaceAll(valid, "16384", "0"),
		"invalid page size": strings.ReplaceAll(valid, "16384", "12345"),
	} {
		t.Run(name, func(t *testing.T) {
			free, measured := darwinFreeMemory(16<<30, output)
			if measured || free != 0 {
				t.Fatalf("invalid counters reported %d available bytes, measured=%v", free, measured)
			}
		})
	}
	if free, measured := darwinFreeMemory(0, valid); measured || free != 0 {
		t.Fatalf("unknown total reported %d available bytes, measured=%v", free, measured)
	}
}

func TestDarwinFreeMemory_ExhaustedMachineIsAReading(t *testing.T) {
	output := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 0.\nPages inactive: 0.\n"
	free, measured := darwinFreeMemory(16<<30, output)
	if !measured || free != 0 {
		t.Fatalf("exhausted host reported %d available bytes, measured=%v", free, measured)
	}
}

func TestSampleHost_NeverClaimsAMeasurementItDoesNotHave(t *testing.T) {
	stat, err := sampleHost()
	if err != nil {
		t.Fatalf("sample host: %v", err)
	}
	if !stat.MemoryMeasured || stat.FreeMemoryBytes > stat.TotalMemoryBytes || stat.TotalMemoryBytes == 0 {
		t.Fatalf("invalid live memory sample: %+v", stat)
	}
	t.Logf("available=%d total=%d", stat.FreeMemoryBytes, stat.TotalMemoryBytes)
}

func TestSampleDarwinHost_RejectsFailedMemoryRead(t *testing.T) {
	readErr := errors.New("memory sensor unavailable")
	stat, err := sampleDarwinHost(func(ctx context.Context) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("memory command has no deadline")
		}
		return []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\nPages inactive: 20.\n"), readErr
	})
	if !errors.Is(err, readErr) || stat.MemoryMeasured || stat.FreeMemoryBytes != 0 {
		t.Fatalf("failed read returned stat=%+v, err=%v", stat, err)
	}
}

func TestSampleDarwinHost_RejectsMalformedMemoryRead(t *testing.T) {
	stat, err := sampleDarwinHost(func(context.Context) ([]byte, error) {
		return []byte("62"), nil
	})
	if err == nil || stat.MemoryMeasured || stat.FreeMemoryBytes != 0 {
		t.Fatalf("malformed read returned stat=%+v, err=%v", stat, err)
	}
}

func BenchmarkDarwinHostSample(b *testing.B) {
	for b.Loop() {
		if _, err := sampleHost(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDarwinMemorySample_QueuesUntilRecovery(t *testing.T) {
	d := newHeadroomDaemon(t, 8, DefaultHeadroomFraction)
	inactive := uint64(245760)
	var readErr error
	d.sampler = hostSamplerFunc(func() (HostStat, error) {
		stat := HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30}
		if readErr != nil {
			return stat, readErr
		}
		output := fmt.Sprintf("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 4096.\nPages inactive: %d.\n", inactive)
		stat.FreeMemoryBytes, stat.MemoryMeasured = darwinFreeMemory(stat.TotalMemoryBytes, output)
		if !stat.MemoryMeasured {
			t.Fatal("invalid memory fixture")
		}
		return stat, nil
	})
	d.refreshHeadroom()
	holder, _, err := d.ledger.Submit(admission.Request{ID: "small-worker", Cores: 0.1, MemoryBytes: 32 << 20})
	if err != nil || holder.Kind != admission.DecisionGranted {
		t.Fatalf("small worker: %s, %v", holder.Kind, err)
	}

	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	waiter := newConn(d, server)
	waiter.runID, waiter.role = "memory-worker", roleWaiter
	waiter.resources = wingwire.HostResources{MemoryBytes: 2 << 30}
	d.byRun[waiter.runID] = waiter
	decision, _, err := d.ledger.Submit(admission.Request{ID: waiter.runID, MemoryBytes: 2 << 30})
	if err != nil || decision.Kind != admission.DecisionQueued {
		t.Fatalf("memory worker under pressure: %s, %v", decision.Kind, err)
	}

	inactive = 524288
	readErr = errors.New("memory read failed")
	before := d.appliedMem
	d.refreshHeadroom()
	if d.appliedMem != before || waiter.role != roleWaiter || len(d.ledger.Snapshot().Waiters) != 1 {
		t.Fatal("failed memory read changed headroom or admitted the waiter")
	}

	messages := make(chan wingwire.Message, 1)
	go func() {
		message, err := newConn(d, peer).readMessage()
		if err != nil {
			t.Errorf("read recovery grant: %v", err)
		}
		messages <- message
	}()
	readErr = nil
	d.refreshHeadroom()
	select {
	case message := <-messages:
		grant, ok := message.(*wingwire.Grant)
		if !ok || grant.RunID != waiter.runID {
			t.Fatalf("recovery delivered %T, want a grant for %s", message, waiter.runID)
		}
	case <-time.After(time.Second):
		t.Fatal("memory recovery did not deliver a grant")
	}
}
