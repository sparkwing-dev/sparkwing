package nodemetrics

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type Sample struct {
	Valid         bool
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

type Sink interface {
	Push(context.Context, Sample) error
}

// ErrSinkFull is what a Sink returns to stop receiving samples for good.
var ErrSinkFull = errors.New("nodemetrics: sink holds no more samples")

const defaultInterval = 2 * time.Second

var intervalNanos atomic.Int64

func SetIntervalForTest(d time.Duration) (restore func()) {
	previous := intervalNanos.Swap(int64(d))
	return func() { intervalNanos.Store(previous) }
}

func Interval() time.Duration {
	if d := time.Duration(intervalNanos.Load()); d > 0 {
		return d
	}
	return defaultInterval
}

type reading struct {
	at        time.Time
	cpu       time.Duration
	memory    int64
	valid     bool
	processes map[int]processSample
}

var usageReader = func() reading { return processTreeUsage(os.Getpid()) }

func intervalSample(previous, current reading) Sample {
	sample := Sample{TS: current.at}
	if current.valid && current.memory >= 0 {
		sample.MemoryBytes = current.memory
	}
	sample.Valid = previous.valid && current.valid && previous.cpu >= 0 && current.cpu >= previous.cpu && previous.memory >= 0 && current.memory >= 0 && current.at.After(previous.at)
	if sample.Valid {
		sample.CPUMillicores = intervalMillicores(current.cpu-previous.cpu, current.at.Sub(previous.at))
	}
	return sample
}

// Attach samples a dedicated node process until finish collects its final reading.
// Finish joins collection and returns the first delivery error, including on repeated calls.
func Attach(ctx context.Context, sink Sink) (finish func() error) {
	previous := usageReader()
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var firstError error
	full := false
	push := func(sample Sample) {
		if full {
			return
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := sink.Push(writeCtx, sample)
		if errors.Is(err, ErrSinkFull) {
			full = true
			return
		}
		if firstError == nil {
			firstError = err
		}
	}
	if !previous.valid || previous.cpu < 0 || previous.memory < 0 {
		push(Sample{TS: previous.at})
	}
	observed := make(map[int]processSample)
	for pid, p := range previous.processes {
		observed[pid] = p
	}
	incomplete := false
	initialMemory := previous.memory
	initialMemoryValid := previous.valid && initialMemory >= 0
	go func() {
		defer close(done)
		ticker := time.NewTicker(Interval())
		defer ticker.Stop()
		collect := func(final bool) {
			current := usageReader()
			sample := intervalSample(previous, current)
			if initialMemoryValid {
				sample.MemoryBytes = max(sample.MemoryBytes, initialMemory)
				initialMemoryValid = false
			}
			if current.valid {
				for pid, p := range observed {
					q, exists := current.processes[pid]
					if !exists || p.birth != q.birth || p.parent != q.parent {
						incomplete = true
					}
				}
				for pid, p := range current.processes {
					observed[pid] = p
				}
			}
			if incomplete || (final && len(current.processes) > 1) {
				sample.Valid = false
			}
			push(sample)
			previous = current
		}
		for {
			select {
			case <-stop:
				collect(true)
				return
			case <-ticker.C:
				collect(false)
			}
		}
	}()
	return func() error {
		once.Do(func() { close(stop) })
		<-done
		return firstError
	}
}

func intervalMillicores(cpu, wall time.Duration) int64 {
	if wall <= 0 {
		return 0
	}
	millicores := cpu.Seconds() / wall.Seconds() * 1000.0
	if millicores < 0 {
		return 0
	}
	if hostMilli := int64(runtime.NumCPU()) * 1000; millicores > float64(hostMilli) {
		return hostMilli
	}
	return int64(millicores)
}
