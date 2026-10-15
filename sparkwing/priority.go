package sparkwing

// Priority sets this run's local admission priority. Higher values admit
// before lower values; equal values keep FIFO order.
func (p *Plan) Priority(n int) *Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.priority = n
	p.prioritySet = true
	return p
}
