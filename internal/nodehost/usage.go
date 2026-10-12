package nodehost

import (
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
)

// Usage is what one node's process session consumed.
type Usage struct {
	// CPUTime is user plus system time across every session member,
	// including children the members waited for.
	CPUTime time.Duration
	// MaxRSSBytes is the highest summed resident set seen across the
	// session's live members, or the leader's own peak when that is higher.
	MaxRSSBytes int64
	Wall        time.Duration
	// SessionWide is false when no session snapshot succeeded, so the figures
	// cover only the leader and the children it reaped. Windows always
	// reports false.
	SessionWide bool
}

// UsageSampler measures a process session from start until Stop. Members that
// escape the session with setsid are not counted, matching what a session kill
// reaches.
//
//	sampler := StartUsage(cmd.Process.Pid)
//	state, _ := cmd.Process.Wait()
//	usage := sampler.Stop(state)
type UsageSampler struct {
	snapshot func(int) (map[int]nodemetrics.SessionProcess, bool)
	sid      int
	started  time.Time
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	result   Usage

	members  map[int]nodemetrics.SessionProcess
	departed time.Duration
	cpu      time.Duration
	peakRSS  int64
	leader   time.Duration
	sampled  bool
}

// StartUsage begins sampling the session whose id is sid, which is the
// leader's pid for a process started with setsid. It samples at
// nodemetrics.Interval.
func StartUsage(sid int) *UsageSampler {
	return startUsage(sid, nodemetrics.SessionProcesses)
}

func startUsage(sid int, snapshot func(int) (map[int]nodemetrics.SessionProcess, bool)) *UsageSampler {
	s := &UsageSampler{
		snapshot: snapshot,
		sid:      sid,
		started:  time.Now(),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	s.sample()
	go s.run(nodemetrics.Interval())
	return s
}

func (s *UsageSampler) run(interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.sample()
		}
	}
}

// Stop takes a final sample and returns the session's usage. Pass the
// leader's state once it has been waited for; its rusage covers the time
// between the last sample and its exit. Later calls return the first result.
func (s *UsageSampler) Stop(leader *os.ProcessState) Usage {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		s.sample()
		s.result = s.finish(leader)
	})
	return s.result
}

func (s *UsageSampler) sample() {
	current, ok := s.snapshot(s.sid)
	if !ok {
		return
	}
	for pid, old := range s.members {
		if now, alive := current[pid]; alive && now.Birth == old.Birth {
			continue
		}
		// safety: a member whose parent is still in the session was reaped
		// there, so its time already sits in that parent's Reaped.
		if parent, alive := current[old.Parent]; alive && s.sameMember(old.Parent, parent) {
			continue
		}
		s.departed += old.CPU + old.Reaped
	}
	live, rss := time.Duration(0), int64(0)
	for _, p := range current {
		live += p.CPU + p.Reaped
		rss += p.RSS
	}
	if p, ok := current[s.sid]; ok {
		s.leader = p.CPU + p.Reaped
	}
	s.cpu = max(s.cpu, s.departed+live)
	s.peakRSS = max(s.peakRSS, rss)
	s.members = current
	s.sampled = true
}

func (s *UsageSampler) sameMember(pid int, now nodemetrics.SessionProcess) bool {
	old, seen := s.members[pid]
	return seen && old.Birth == now.Birth
}

func (s *UsageSampler) finish(leader *os.ProcessState) Usage {
	u := Usage{CPUTime: s.cpu, MaxRSSBytes: s.peakRSS, Wall: time.Since(s.started), SessionWide: s.sampled}
	if leader == nil {
		return u
	}
	// safety: the leader's last sampled time is already in the total, so only
	// what it used after that sample is added.
	if final := leader.UserTime() + leader.SystemTime(); final > s.leader {
		u.CPUTime += final - s.leader
	}
	u.MaxRSSBytes = max(u.MaxRSSBytes, maxRSSBytes(leader))
	return u
}
