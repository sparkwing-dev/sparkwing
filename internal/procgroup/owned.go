package procgroup

import "sync"

var owned = struct {
	mu     sync.Mutex
	groups map[int]int
}{groups: map[int]int{}}

// Own records a process group this process started and must not outlive.
// KillOwned reaches every group still held. Own returns the release to call
// once the group has been reaped; a group owned more than once stays held
// until every release runs.
func Own(group int) func() {
	if group <= 1 {
		return func() {}
	}
	owned.mu.Lock()
	owned.groups[group]++
	owned.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			owned.mu.Lock()
			defer owned.mu.Unlock()
			if owned.groups[group] <= 1 {
				delete(owned.groups, group)
				return
			}
			owned.groups[group]--
		})
	}
}

// Owned lists the process groups currently held.
func Owned() []int {
	owned.mu.Lock()
	defer owned.mu.Unlock()
	groups := make([]int, 0, len(owned.groups))
	for group := range owned.groups {
		groups = append(groups, group)
	}
	return groups
}

// KillOwned sends SIGKILL to every owned process group. It is the last thing
// a process does before it dies from a signal or abandons its work, so the
// groups it started cannot outlive it; a group that is already gone is not an
// error.
func KillOwned() {
	for _, group := range Owned() {
		killOwnedGroup(group)
	}
}

// ForwardTerminationToOwned makes SIGTERM reap the owned groups before it
// ends this process. The signal is re-raised with its default action once
// the groups are gone, so the exit status a supervisor observes is the same
// as if the process had never handled it.
func ForwardTerminationToOwned() { forwardTerminationToOwned() }
