package nodehost

import "runtime"

// AwaitsHeld closes its channel once n awaits hold places on c.
func AwaitsHeld(c *Claim, n int) <-chan struct{} {
	held := make(chan struct{})
	go func() {
		for len(c.awaitSlots) < n {
			runtime.Gosched()
		}
		close(held)
	}()
	return held
}

// TimeoutLayer is the timeout layer around next.
func TimeoutLayer(next Executor) Executor { return timeoutLayer(nil, next) }
