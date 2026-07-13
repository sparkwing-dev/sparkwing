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
)

// ProtocolMajor is the wire protocol's compatibility version. A daemon
// and a client interoperate exactly when they share this value; a
// mismatch means the client must trigger a daemon takeover (client
// newer) or fail with a clear upgrade message (client older).
const ProtocolMajor = 1

// LeaseTokenEnv is the environment variable a parent run sets on child
// processes it spawns. A child that finds it presents the token in
// [AdmissionRequest].ParentLeaseToken; the daemon attaches the child to
// the parent's live lease instead of charging the host budget twice.
// This single variable is the whole inheritance surface -- child runs
// carry no other admission state.
const LeaseTokenEnv = "SPARKWING_LEASE_TOKEN"

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
