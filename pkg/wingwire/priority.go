package wingwire

// PriorityFront and PriorityBack are the two relative priority modes an
// operator can ask for instead of naming a number. They are resolved once,
// against the queue as it stands when the run starts, and the run carries the
// resulting integer for its whole life.
const (
	PriorityFront = "front"
	PriorityBack  = "back"
)

// ResolveRelativePriority turns [PriorityFront] or [PriorityBack] into the
// integer priority that sorts ahead of, or behind, every waiter in waiters.
// An empty queue counts as priority zero, so the two modes answer +1 and -1
// there. Any other mode answers 0.
//
// Admission orders waiters by priority descending and breaks ties FIFO, so one
// past the extreme is enough to clear the whole queue, and answering the
// extreme itself would only tie.
func ResolveRelativePriority(waiters []Waiter, mode string) int {
	switch mode {
	case PriorityFront:
		return extremePriority(waiters, true) + 1
	case PriorityBack:
		return extremePriority(waiters, false) - 1
	default:
		return 0
	}
}

func extremePriority(waiters []Waiter, highest bool) int {
	if len(waiters) == 0 {
		return 0
	}
	best := waiters[0].Priority
	for _, w := range waiters[1:] {
		if highest && w.Priority > best {
			best = w.Priority
		}
		if !highest && w.Priority < best {
			best = w.Priority
		}
	}
	return best
}
