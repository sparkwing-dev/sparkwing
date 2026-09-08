package wingwire

import "testing"

func TestResolveRelativePriority(t *testing.T) {
	waiters := func(ps ...int) []Waiter {
		out := make([]Waiter, len(ps))
		for i, p := range ps {
			out[i] = Waiter{RunID: "r", Priority: p}
		}
		return out
	}
	cases := []struct {
		name    string
		waiters []Waiter
		mode    string
		want    int
	}{
		{"empty queue front", nil, PriorityFront, 1},
		{"empty queue back", nil, PriorityBack, -1},
		{"front clears the highest", waiters(0, 3, 1), PriorityFront, 4},
		{"back clears the lowest", waiters(0, 3, 1), PriorityBack, -1},
		{"front over an all-negative queue", waiters(-5, -2), PriorityFront, -1},
		{"back under an all-positive queue", waiters(5, 2), PriorityBack, 1},
		{"unknown mode", waiters(7), "sideways", 0},
		{"empty mode", waiters(7), "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRelativePriority(tc.waiters, tc.mode); got != tc.want {
				t.Errorf("ResolveRelativePriority(%v, %q) = %d, want %d", tc.waiters, tc.mode, got, tc.want)
			}
		})
	}
}

func TestResolveRelativePriorityBeatsEveryWaiter(t *testing.T) {
	queue := []Waiter{{Priority: -3}, {Priority: 12}, {Priority: 0}}
	front := ResolveRelativePriority(queue, PriorityFront)
	back := ResolveRelativePriority(queue, PriorityBack)
	for _, w := range queue {
		if front <= w.Priority {
			t.Errorf("front %d does not outrank waiter %d", front, w.Priority)
		}
		if back >= w.Priority {
			t.Errorf("back %d does not yield to waiter %d", back, w.Priority)
		}
	}
}
