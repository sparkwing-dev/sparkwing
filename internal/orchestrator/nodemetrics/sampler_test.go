package nodemetrics

import (
	"context"
	"sync"
)

type captureSink struct {
	mu      sync.Mutex
	samples []Sample
}

func (s *captureSink) Push(_ context.Context, sample Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
	return nil
}

func (s *captureSink) snapshot() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sample(nil), s.samples...)
}

type sinkFunc func(context.Context, Sample) error

func (f sinkFunc) Push(ctx context.Context, s Sample) error { return f(ctx, s) }
