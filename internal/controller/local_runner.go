package controller

import (
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LocalRunner describes the machine a private controller executes on.
type LocalRunner struct {
	Name   string
	Labels []string
}

// WithLocalRunner lists this machine as a runner in the private controller's
// inventory, with the runner.labels the daemon read at start.
func (s *Server) WithLocalRunner(runner LocalRunner) *Server {
	runner.Labels = slices.Clone(runner.Labels)
	s.localRunner = &runner
	return s
}

// safety: the store sees the local executor only through claim awards, so the
// machine vanished when idle, carried no labels, and kept one row per daemon
// lifetime whose last_seen froze at that daemon's final claim. The daemon
// answering this read is the runner, so it is seen now.
func mergeLocalRunner(snapshot capability.RunnerSnapshot, team string, local *LocalRunner) capability.RunnerSnapshot {
	if local == nil || team != string(store.DefaultTeam) {
		return snapshot
	}
	row := capability.RunnerObservation{
		ID: "local:" + local.Name, Team: team, Location: "local", LastSeen: snapshot.Now, ActiveJobs: []string{},
		Runner: nodeproto.RunnerIdentity{Name: local.Name, Type: "local", Labels: slices.Clone(local.Labels)},
	}
	runners := make([]capability.RunnerObservation, 0, len(snapshot.Runners)+1)
	for _, r := range snapshot.Runners {
		if r.Runner.Type != "local" {
			runners = append(runners, r)
			continue
		}
		row.ActiveJobs = append(row.ActiveJobs, r.ActiveJobs...)
		row.MaxConcurrent = max(row.MaxConcurrent, r.MaxConcurrent)
		if r.Headroom != nil && (row.Headroom == nil || r.Headroom.ObservedAt.After(row.Headroom.ObservedAt)) {
			row.Headroom = r.Headroom
		}
	}
	slices.Sort(row.ActiveJobs)
	row.ActiveJobs = slices.Compact(row.ActiveJobs)
	snapshot.Runners = append(runners, row)
	return snapshot
}
