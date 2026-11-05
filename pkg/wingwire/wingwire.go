// Package wingwire defines JSON data shared by the local controller HTTP API,
// admission clients, and queue views. HTTP transport and peer authentication live
// in the controller and client implementations.
package wingwire

import (
	"encoding/base64"
	"slices"
)

// BuildIdentity changes whenever same-major wire behavior changes. It lets
// source and installed builds compiled from the same protocol implementation
// recognize each other even when their display versions differ.
const BuildIdentity = "wingwire-v4-durable-http"

// ProtocolMajor identifies the durable HTTP controller contract.
const ProtocolMajor = 4

// MinProtocolMajor is the oldest controller contract this build serves.
// Earlier connection-based admission clients must upgrade their pipeline pin.
const MinProtocolMajor = 4

// ProtocolFloor is one cliff in the wire protocol's history: the lowest
// released SDK version whose clients speak Major.
type ProtocolFloor struct {
	Major      int
	MinVersion string
}

// ProtocolFloors is the release-to-major table, oldest major first. A
// pipeline binary compiled against a release below a daemon's floor speaks
// an older major, so the daemon refuses it and restarting cannot resolve it --
// the repo's own .sparkwing/go.mod pin has to move.
//
// It is a table rather than a single boundary because a diagnosis involves
// two versions that are both free to be old. The binary doing the reasoning
// is a third party: a daemon two majors behind the current release still
// locks out every pin behind *it*, and a boundary constant naming only the
// current cliff cannot see that at all.
//
// Every lookup here is keyed by major, never by version. A table can place a
// release only below its newest row -- which major a later release speaks was
// decided after the table was written -- so mapping a version to a major
// silently returns a floor dressed up as an answer. Ask instead for the
// release a known major starts at, and compare versions against that.
type ProtocolFloors []ProtocolFloor

var releasedProtocolFloors = ProtocolFloors{
	{Major: 1, MinVersion: "v0.0.0"},
	{Major: 2, MinVersion: "v0.22.0"},
	{Major: 3, MinVersion: "v0.24.0"},
}

// ReleasedProtocolFloors returns the release-to-major table this build
// shipped with. An unreleased major has no release floor yet.
func ReleasedProtocolFloors() ProtocolFloors {
	return slices.Clone(releasedProtocolFloors)
}

// MinVersionSpeaking returns the lowest released SDK version whose clients
// speak protocol major. ok is false when the table has no row for major,
// which is what a daemon newer than the binary reading it looks like; only
// that daemon's own version is then known to name a release speaking to it.
//
// Because the table's majors run 1..n with ascending floors, a release below
// the answer speaks a major below major, and a release at or above it speaks
// major or newer. That is the comparison a caller holding a peer's major
// wants, and unlike a version-to-major lookup it stays exact.
func (f ProtocolFloors) MinVersionSpeaking(major int) (version string, ok bool) {
	for _, floor := range f {
		if floor.Major == major {
			return floor.MinVersion, true
		}
	}
	return "", false
}

// Newest returns the table's newest row: the highest protocol major it
// covers and the release that introduced it. ok is false for an empty table.
// A caller comparing itself against a peer uses this to tell whether the peer
// speaks a major this build has heard of at all.
func (f ProtocolFloors) Newest() (floor ProtocolFloor, ok bool) {
	if len(f) == 0 {
		return ProtocolFloor{}, false
	}
	return f[len(f)-1], true
}

// NodeHostParticipant is the [AdmissionRequest].RunID of the lease a node of
// runID takes for its own host capacity, with any concurrency group it joins.
func NodeHostParticipant(runID, nodeID string) HolderRef {
	return HolderRef(runID + "/node-host/" + base64.RawURLEncoding.EncodeToString([]byte(nodeID)))
}

// NodeSemaphoreParticipant is the [AdmissionRequest].RunID of the lease a
// node takes for its concurrency group alone, when its run's lease already
// holds the host.
func NodeSemaphoreParticipant(runID, nodeID string) HolderRef {
	return HolderRef(runID + "/node-semaphore/" + base64.RawURLEncoding.EncodeToString([]byte(nodeID)))
}

// PlanSemaphoresParticipant is the [AdmissionRequest].RunID of the lease an
// attached run takes for the plan concurrency groups its parent does not hold.
func PlanSemaphoresParticipant(runID string) HolderRef { return HolderRef(runID + "/plan") }
