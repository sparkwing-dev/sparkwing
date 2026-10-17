package cluster

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestRunnerLocalCapacityStartsVerifiedControllerBeforeRequery(t *testing.T) {
	home := t.TempDir()
	queries, resolutions, starts := 0, 0, 0
	capacity, available, err := runnerLocalCapacity(t.Context(), home, "v1.2.3",
		func(_ context.Context, pin engine.Pin, target releaseasset.Target) (engine.Resolved, error) {
			resolutions++
			if queries != 1 || pin.SDK != engine.GoSDK || pin.Version != "v1.2.3" || target.Binary != releaseasset.Sparkwing {
				t.Fatalf("controller selection: queries=%d pin=%+v target=%+v", queries, pin, target)
			}
			return engine.Resolved{Executable: "/verified/sparkwing"}, nil
		},
		func(_ context.Context, options wingdclient.Options) (wingwire.QueueState, error) {
			queries++
			if options.Home != home || options.Version != "v1.2.3" {
				t.Fatalf("query changed the machine: %+v", options)
			}
			if queries == 1 {
				return wingwire.QueueState{}, wingdclient.ErrNoDaemon
			}
			if starts != 1 {
				t.Fatal("queried before the selected controller was ready")
			}
			return wingwire.QueueState{Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Available: 0}, {Key: "memory", Capacity: 4096, Available: 0}}}, nil
		},
		func(_ context.Context, options wingd.LocalStartOptions) (wingd.LocalStatus, error) {
			starts++
			if options.Home != home || options.Version != "v1.2.3" || options.Executable != "/verified/sparkwing" || options.Standalone {
				t.Fatalf("startup did not use the selected machine controller: %+v", options)
			}
			return wingd.LocalStatus{Running: true}, nil
		})
	if err != nil || resolutions != 1 || starts != 1 || queries != 2 || capacity.Cores != 8 || capacity.MemoryBytes != 4096 || available != (match.Resources{}) {
		t.Fatalf("capacity=%+v available=%+v queries=%d resolutions=%d starts=%d error=%v", capacity, available, queries, resolutions, starts, err)
	}
}

func TestRunnerLocalCapacityNeverSpawnsAroundAnUnreachableOwner(t *testing.T) {
	boundary := errors.New("elected owner is unreachable")
	_, _, err := runnerLocalCapacity(t.Context(), t.TempDir(), "v1.2.3",
		func(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
			t.Fatal("selected a replacement for an unreachable owner")
			return engine.Resolved{}, nil
		},
		func(context.Context, wingdclient.Options) (wingwire.QueueState, error) {
			return wingwire.QueueState{}, boundary
		},
		func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
			t.Fatal("spawned another owner")
			return wingd.LocalStatus{}, nil
		})
	if !errors.Is(err, boundary) {
		t.Fatal(err)
	}
}

func TestRunnerShareKeepsPhysicalCapacityWhenFullyReserved(t *testing.T) {
	capacity := match.Resources{Cores: 8, MemoryBytes: 8 << 30}
	reserve, err := userconfig.ParseBudget("100%,100%")
	if err != nil {
		t.Fatal(err)
	}
	physical, available := runnerShare(capacity, capacity, userconfig.Budget{}, reserve)
	if physical != capacity || available != (match.Resources{}) || match.Fits(physical, available, match.Resources{Cores: 1}).OK() {
		t.Fatalf("full reservation became unknown capacity: physical=%+v free=%+v", physical, available)
	}
}

func TestSampledCapacityUsesMeasuredHeadroom(t *testing.T) {
	stat := wingd.HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, BusyCores: 7, CPUMeasured: true, LoadAverage: 2, LoadMeasured: true, FreeMemoryBytes: 3 << 30, MemoryMeasured: true}
	physical, free, err := sampledCapacity(stat)
	if err != nil || physical.Cores != 8 || free.Cores != 1 || free.MemoryBytes != 3<<30 {
		t.Fatalf("sample=%+v physical=%+v free=%+v error=%v", stat, physical, free, err)
	}
	stat.CPUMeasured = false
	_, free, err = sampledCapacity(stat)
	if err != nil || free.Cores != 6 {
		t.Fatalf("load fallback=%+v error=%v", free, err)
	}
	for _, cores := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		stat.TotalCores = cores
		if _, _, err := sampledCapacity(stat); err == nil {
			t.Fatalf("invalid capacity %v admitted", cores)
		}
	}
}

func FuzzRunnerSharePreservesHeadroom(f *testing.F) {
	f.Add(uint16(800), uint16(250), uint16(60), uint8(50), uint8(25))
	f.Add(uint16(800), uint16(0), uint16(0), uint8(100), uint8(100))
	f.Fuzz(func(t *testing.T, total, freeCPU, freeMemory uint16, contribution, reserve uint8) {
		capacity := match.Resources{Cores: (float64(total) + 1) / 100, MemoryBytes: int64(total) + 1}
		available := match.Resources{Cores: min(capacity.Cores, float64(freeCPU)/100), MemoryBytes: min(capacity.MemoryBytes, int64(freeMemory))}
		share := userconfig.Budget{CoresFraction: float64(contribution%100+1) / 100, MemoryFraction: float64(contribution%100+1) / 100}
		keep := userconfig.Budget{CoresFraction: float64(reserve%100+1) / 100, MemoryFraction: float64(reserve%100+1) / 100}
		physical, offered := runnerShare(capacity, available, share, keep)
		if physical != capacity || offered.Cores < 0 || offered.Cores > available.Cores || offered.MemoryBytes < 0 || offered.MemoryBytes > available.MemoryBytes || offered.Cores > share.CapCores(capacity.Cores) || offered.MemoryBytes > int64(share.CapMemory(uint64(capacity.MemoryBytes))) {
			t.Fatalf("capacity=%+v available=%+v share=%+v keep=%+v offered=%+v", capacity, available, share, keep, offered)
		}
	})
}
