//go:build linux || windows

package wingd

func (p *procSampler) forget(pid int) {
	p.mu.Lock()
	delete(p.last, pid)
	delete(p.tree, pid)
	p.mu.Unlock()
}

func trackedPIDs(pids []int) map[int]struct{} {
	tracked := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		tracked[pid] = struct{}{}
	}
	return tracked
}

func (p *procSampler) pruneTreeLocked(root int, live map[int]struct{}) {
	for pid := range p.tree[root] {
		if _, ok := live[pid]; !ok {
			delete(p.last, pid)
		}
	}
	p.tree[root] = live
}
