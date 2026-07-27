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
// daemon). Compatibility is governed by [ProtocolMajor] alone: a daemon
// serves any client within the same protocol major, because compiled
// pipeline binaries pin SDK versions and may be older than the daemon.
// The binary version travels alongside for observability and for the
// newer-client takeover decision, never for compatibility gating.
package wingwire

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/semver"
)

// ProtocolMajor is the wire protocol's compatibility version. A daemon
// and a client interoperate exactly when they share this value; a
// mismatch means the client must trigger a daemon takeover (client
// newer) or fail with a clear upgrade message (client older).
const ProtocolMajor = 2

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
type ProtocolFloors []ProtocolFloor

// releasedProtocolFloors is the table this build shipped with. A bump of
// [ProtocolMajor] appends a row naming the release that carries the bump,
// and every past row stays: explaining a refusal between two older builds
// needs the cliff between them long after both are behind.
var releasedProtocolFloors = ProtocolFloors{
	{Major: 1, MinVersion: "v0.0.0"},
	{Major: 2, MinVersion: "v0.22.0"},
}

// ReleasedProtocolFloors returns the release-to-major table this build
// shipped with, whose newest row is [ProtocolMajor].
func ReleasedProtocolFloors() ProtocolFloors {
	return releasedProtocolFloors
}

// MajorSpokenBy reports the wire protocol major a client or daemon built
// from SDK release version speaks. version is a semver string as it appears
// in a .sparkwing/go.mod require; ok is false when it does not parse, which
// is not evidence about any major, so callers stay silent rather than
// accusing a repo they could not read.
//
// The answer is a floor rather than an equality: a version past the newest
// row reports that row's major, because a table cannot describe a major
// first released after it was written. Callers therefore compare two majors
// -- a daemon's against a pin's -- and never test one against the
// compiled-in [ProtocolMajor], which describes only the cliff current at
// compile time and goes blind at the next bump.
func (f ProtocolFloors) MajorSpokenBy(version string) (major int, ok bool) {
	if len(f) == 0 || !semver.IsValid(version) {
		return 0, false
	}
	canon := semver.Canonical(version)
	major = f[0].Major
	for _, floor := range f {
		if semver.Compare(canon, floor.MinVersion) >= 0 {
			major = floor.Major
		}
	}
	return major, true
}

// MinVersionSpeaking returns the lowest released SDK version whose clients
// speak protocol major. ok is false when the table has no row for major,
// which is what a daemon newer than the binary reading it looks like; only
// that daemon's own version is then known to name a release speaking to it.
func (f ProtocolFloors) MinVersionSpeaking(major int) (version string, ok bool) {
	for _, floor := range f {
		if floor.Major == major {
			return floor.MinVersion, true
		}
	}
	return "", false
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
	TypeReattach         MessageType = "reattach"
	TypeDrainRequest     MessageType = "drain_request"
	TypeDrainAck         MessageType = "drain_ack"
	TypeQueueState       MessageType = "queue_state"
	TypeCancelLease      MessageType = "cancel_lease"
	TypeCancelLeaseAck   MessageType = "cancel_lease_ack"
	TypeCancel           MessageType = "cancel"
	TypeStatsReset       MessageType = "stats_reset"
	TypeStatsResetAck    MessageType = "stats_reset_ack"
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
	default:
		return nil, fmt.Errorf("wingwire: unknown message type %q (peer speaks a different protocol major than %d)", t, ProtocolMajor)
	}
}
