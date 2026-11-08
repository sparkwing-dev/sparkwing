package wingd

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type connRole int

const (
	roleNone connRole = iota
	roleWaiter
	roleHolder
)

func (r connRole) String() string {
	switch r {
	case roleWaiter:
		return "queued"
	case roleHolder:
		return "holding admission"
	default:
		return "idle"
	}
}

const maxFrame = 8 << 20

type conn struct {
	measurementPID   int
	measurementBirth string
	// safety: host-run commands by PID and birth live in memory; a restart loses their measurement, not capacity.
	commands map[int]string
	ticketID string

	runID        string
	journalRunID string
	ownerRunID   string
	displayRunID string
	pipeline     string
	priority     int
	class        string
	pid          int
	role         connRole
	leaseID      admission.LeaseID
	members      []string
	resources    wingwire.HostResources
	sems         []string
	startAt      time.Time

	costSource         string
	repo               string
	expectedDurationMS int64
	driftWarning       string
	origin             wingwire.Origin

	stalled  bool
	lowSince time.Time

	expectedP99MS int64
	sampleCount   int

	holdSampledMS    int64
	holdSaturatedMS  int64
	contended        bool
	contentionReason string

	finalizable bool
}

func (c *conn) samplePID() int {
	if c.measurementPID == 0 {
		return c.pid
	}
	actual, err := admissionProcessBirth(c.measurementPID)
	if err != nil || actual != c.measurementBirth {
		return 0
	}
	return c.measurementPID
}

func (c *conn) measuredCommands() []int {
	var live []int
	for pid, birth := range c.commands {
		if actual, err := admissionProcessBirth(pid); err != nil || actual != birth {
			delete(c.commands, pid)
			continue
		}
		live = append(live, pid)
	}
	return live
}
