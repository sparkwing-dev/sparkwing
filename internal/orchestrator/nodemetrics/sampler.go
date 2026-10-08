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
// Finish joins collection and returns delivery counts and the first loss on every call.
func Attach(ctx context.Context, sink Sink) (finish func() (Delivery, error)) {
	return attach(ctx, sink, func() reading { return processTreeUsage(os.Getpid()) })
}

// Delivery reports attempted and lost samples, excluding a full sink.
// LostInvalid counts lost samples that marked the measurement invalid; losing
// one loses an exclusion, not just a reading.
type Delivery struct {
	Attempted   int64
	Lost        int64
	LostInvalid int64
}

func attach(ctx context.Context, sink Sink, read func() reading) (finish func() (Delivery, error)) {
	previous := read()
	stop, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var firstError error
	var delivery Delivery
	full := false
	// safety: a full sink drops readings by design, but an invalid one still carries an exclusion the finish must record.
	dropFull := func(sample Sample) bool {
		if !sample.Valid {
			delivery.LostInvalid++
			if firstError == nil {
				firstError = ErrSinkFull
			}
		}
		return false
	}
	push := func(sample Sample) bool {
		if full {
			return dropFull(sample)
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := sink.Push(writeCtx, sample)
		if errors.Is(err, ErrSinkFull) {
			full = true
			return dropFull(sample)
		}
		delivery.Attempted++
		if err != nil {
			delivery.Lost++
			if !sample.Valid {
				delivery.LostInvalid++
			}
			if firstError == nil {
				firstError = err
			}
			return false
		}
		return true
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
			current := read()
			sample := intervalSample(previous, current)
			if initialMemoryValid {
				sample.MemoryBytes = max(sample.MemoryBytes, initialMemory)
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
			if push(sample) {
				previous = current
				initialMemoryValid = false
			}
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
	return func() (Delivery, error) {
		once.Do(func() { close(stop) })
		<-done
		return delivery, firstError
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
