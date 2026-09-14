package objectguard

import "sync/atomic"

// Coalescer runs one background job at a time without dropping the
// requests it turns away.
//
// A store measurement is the job it exists for: two of them read the
// same tree, so running both is waste, but silently dropping the second
// loses an operator's recovery when the running walk has already passed
// the directory they freed. A turned-away request instead marks the
// running job to run once more when it finishes.
//
// The zero value is ready to use. Safe for concurrent use.
type Coalescer struct {
	running atomic.Bool
	dirty   atomic.Bool
}

// Go runs job in a goroutine when none is running, and otherwise marks
// the running job to repeat once it finishes. It reports whether this
// call started the goroutine; false means the work is covered by one
// already in flight.
func (c *Coalescer) Go(job func()) bool {
	if !c.running.CompareAndSwap(false, true) {
		c.dirty.Store(true)
		return false
	}
	go c.run(job)
	return true
}

// safety: the flag is released before the repeat is considered, so a request
// arriving in that window either finds the job free and starts it or sets the
// flag this loop is about to read; neither path drops the work.
func (c *Coalescer) run(job func()) {
	for {
		c.dirty.Store(false)
		job()
		c.running.Store(false)
		if !c.dirty.Load() {
			return
		}
		if !c.running.CompareAndSwap(false, true) {
			return
		}
	}
}

// Running reports whether a job is in flight.
func (c *Coalescer) Running() bool { return c.running.Load() }
