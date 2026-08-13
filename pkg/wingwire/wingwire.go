// Package wingwire defines the JSON wire protocol spoken between the
// local admission daemon and its clients (run processes, the CLI's
// queue view, and a successor daemon taking over during a version
// upgrade). It contains pure data types and their (de)serialization;
// transport -- sockets, connection lifecycle, dispatch -- lives with
// the daemon and client implementations, not here.
//
// # Framing
//
// Messages are newline-delimited JSON: each message is one [Envelope]
// serialized as a single JSON object followed by one '\n'. encoding/json
// escapes newlines inside strings, so an encoded message never spans
// lines and a reader can frame the stream with nothing more than
// bufio.Scanner's default line splitter. [Encode] emits exactly this
// framing and [Decode] accepts a line with or without its trailing
// newline.
//
// # Versioning
//
// The first message in each direction on a fresh connection is the
// version handshake ([Hello] from the client, [HelloAck] from the
// daemon). The client states the protocol major it speaks and the
// daemon answers on the newest major the two share, anywhere in
// [MinProtocolMajor]..[ProtocolMajor]. Compiled pipeline binaries pin
// SDK versions and keep running long after the daemon upgrades past
// them, so the daemon meets such a client on its own major rather than
// refusing it. The binary version travels alongside for observability
// and for the newer-client takeover decision, never for compatibility
// gating.
package wingwire

import (
	"encoding/json"
	"fmt"
)

// ProtocolMajor is the newest wire protocol major this build speaks.
// Clients send it in [Hello]; a daemon answers on it whenever the client
// speaks it too.
const ProtocolMajor = 3

// MinProtocolMajor is the oldest wire protocol major a daemon of this
// build still serves. Every major from here up to [ProtocolMajor] is
// answered, so a repo whose pinned SDK predates the daemon keeps getting
// admission grants instead of being locked out the moment the daemon
// upgrades. Raising it is what turns a stale pin into a hard failure, so
// raise it only for a change that genuinely cannot be served in the old
// major's terms, and document the cut as a break.
const MinProtocolMajor = 1

// ServedMajor reports the protocol major a daemon of this build answers a
// client on, given the major that client sent in [Hello]. A client inside
// the served range is met on its own major; anything else is answered
// with this build's own, leaving the client to take the daemon over (it
// is newer) or to fail with a version error (it is older than the floor).
// No map from release to protocol major is needed anywhere: every client
// states its own major on every connection.
func ServedMajor(client int) int {
	if client >= MinProtocolMajor && client < ProtocolMajor {
		return client
	}
	return ProtocolMajor
}

// ProtocolFloor is one cliff in the wire protocol's history: the lowest
// released SDK version whose clients speak Major.
type ProtocolFloor struct {
	Major      int
	MinVersion string
}

// ProtocolFloors is the release-to-major table, oldest major first. A
// pipeline binary compiled against a release below a daemon's floor speaks
// an older major, so the daemon refuses it and no takeover can resolve it --
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

// releasedProtocolFloors is the table this build shipped with. A bump of
// [ProtocolMajor] appends a row naming the release that carries the bump,
// and every past row stays: explaining a refusal between two older builds
// needs the cliff between them long after both are behind.
var releasedProtocolFloors = ProtocolFloors{
	{Major: 1, MinVersion: "v0.0.0"},
	{Major: 2, MinVersion: "v0.22.0"},
	{Major: 3, MinVersion: "v0.24.0"},
}

// ReleasedProtocolFloors returns the release-to-major table this build
// shipped with, whose newest row is [ProtocolMajor].
func ReleasedProtocolFloors() ProtocolFloors {
	return releasedProtocolFloors
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

// LeaseTokenEnv is the current execution lease token inherited by child
// processes. A child presents this token as [AdmissionRequest].ParentLeaseToken
// unless [ChildLeaseTokenEnv] carries a more specific child-attach token.
const LeaseTokenEnv = "SPARKWING_LEASE_TOKEN"

// ChildLeaseTokenEnv is the token child Sparkwing runs attach to when it
// differs from the current execution lease. A node can hold a short-lived host
// lease while its children must attach to the run-scope semaphore lease.
const ChildLeaseTokenEnv = "SPARKWING_CHILD_LEASE_TOKEN"

// MessageType discriminates the concrete payload carried by an
// [Envelope].
type MessageType string

const (
	TypeHello            MessageType = "hello"
	TypeHelloAck         MessageType = "hello_ack"
	TypeAdmissionRequest MessageType = "admission_request"
	TypeGrant            MessageType = "grant"
	TypeQueued           MessageType = "queued"
	TypeEvicted          MessageType = "evicted"
	TypeRelease          MessageType = "release"
	TypeGuardComplete    MessageType = "guard_complete"
	TypeGuardCompleteAck MessageType = "guard_complete_ack"
	TypeReattach         MessageType = "reattach"
	TypeDrainRequest     MessageType = "drain_request"
	TypeDrainAck         MessageType = "drain_ack"
	TypeQueueState       MessageType = "queue_state"
	TypeCancelLease      MessageType = "cancel_lease"
	TypeCancelLeaseAck   MessageType = "cancel_lease_ack"
	TypeCancel           MessageType = "cancel"
	TypeStatsReset       MessageType = "stats_reset"
	TypeStatsResetAck    MessageType = "stats_reset_ack"
	TypeLivenessProbe    MessageType = "liveness_probe"
	TypeLivenessAck      MessageType = "liveness_ack"
)

// Message is implemented by every concrete wire message. The
// implementing set is closed: the unexported method keeps arbitrary
// types out so [Decode] can guarantee an exhaustive mapping from
// [MessageType] to concrete type.
type Message interface {
	wireType() MessageType
}

// Envelope is the framing wrapper around every message: the type
// discriminator plus the raw payload. Consumers normally use [Encode]
// and [Decode] instead of touching Envelope directly.
type Envelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Encode serializes m as one framed line: an [Envelope] JSON object
// terminated by '\n', ready to write to the connection as-is.
func Encode(m Message) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("wingwire: Encode: nil message")
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("wingwire: Encode %s: %w", m.wireType(), err)
	}
	line, err := json.Marshal(Envelope{Type: m.wireType(), Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("wingwire: Encode %s: %w", m.wireType(), err)
	}
	return append(line, '\n'), nil
}

// Decode parses one framed line (with or without its trailing newline)
// into the concrete message it carries. Unknown message types are an
// error: within one protocol major the type set only grows, so an
// unknown type means the peer is from a different major.
func Decode(line []byte) (Message, error) {
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, fmt.Errorf("wingwire: Decode envelope: %w", err)
	}
	m, err := emptyMessage(env.Type)
	if err != nil {
		return nil, err
	}
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, m); err != nil {
			return nil, fmt.Errorf("wingwire: Decode %s payload: %w", env.Type, err)
		}
	}
	return m, nil
}

func emptyMessage(t MessageType) (Message, error) {
	switch t {
	case TypeHello:
		return &Hello{}, nil
	case TypeHelloAck:
		return &HelloAck{}, nil
	case TypeAdmissionRequest:
		return &AdmissionRequest{}, nil
	case TypeGrant:
		return &Grant{}, nil
	case TypeQueued:
		return &Queued{}, nil
	case TypeEvicted:
		return &Evicted{}, nil
	case TypeRelease:
		return &Release{}, nil
	case TypeGuardComplete:
		return &GuardComplete{}, nil
	case TypeGuardCompleteAck:
		return &GuardCompleteAck{}, nil
	case TypeReattach:
		return &Reattach{}, nil
	case TypeDrainRequest:
		return &DrainRequest{}, nil
	case TypeDrainAck:
		return &DrainAck{}, nil
	case TypeQueueState:
		return &QueueState{}, nil
	case TypeCancelLease:
		return &CancelLease{}, nil
	case TypeCancelLeaseAck:
		return &CancelLeaseAck{}, nil
	case TypeCancel:
		return &Cancel{}, nil
	case TypeStatsReset:
		return &StatsReset{}, nil
	case TypeStatsResetAck:
		return &StatsResetAck{}, nil
	case TypeLivenessProbe:
		return &LivenessProbe{}, nil
	case TypeLivenessAck:
		return &LivenessAck{}, nil
	default:
		return nil, fmt.Errorf("wingwire: unknown message type %q (peer speaks a different protocol major than %d)", t, ProtocolMajor)
	}
}
