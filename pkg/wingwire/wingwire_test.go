package wingwire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func roundTripMessages() []Message {
	return []Message{
		&Hello{ProtocolMajor: ProtocolMajor, BinaryVersion: "v0.15.6"},
		&HelloAck{ProtocolMajor: ProtocolMajor, BinaryVersion: "v0.16.0", Draining: true},
		&AdmissionRequest{
			RunID:     "deploy-20260710-120000",
			Resources: HostResources{Cores: 2.5, MemoryBytes: 4 << 30},
			Semaphores: []SemaphoreClaim{
				{Name: "deploy-lock", Cost: 1, Capacity: 1, Policy: PolicyQueue, QueueTimeoutMS: 60_000},
				{Name: "db", Cost: 4, Capacity: 8, Policy: PolicyCancelOthers},
			},
			ParentLeaseToken: "lease-abc123",
		},
		&Grant{
			RunID:      "deploy-20260710-120000",
			LeaseToken: "lease-def456",
			Resources:  HostResources{Cores: 2.5, MemoryBytes: 4 << 30},
		},
		&Queued{RunID: "r1", Key: "cores", Position: 2, QueueLength: 3},
		&Evicted{RunID: "r1", Key: "deploy-lock", SupersededBy: "r2", Policy: PolicyCancelOthers},
		&Release{LeaseToken: "lease-def456"},
		&Reattach{LeaseToken: "lease-def456"},
		&DrainRequest{SuccessorVersion: "v0.16.0"},
		&DrainAck{HoldersRemaining: 3},
		&CancelLease{RunID: "deploy-20260710-120000"},
		&CancelLeaseAck{Found: true},
		&Cancel{RunID: "deploy-20260710-120000", Reason: "cancelled via sparkwing runs cancel"},
		&QueueState{
			Resources: []ResourceState{
				{Key: "cores", Capacity: 10, Held: 6.5, Reserved: 2, External: 1.5, Available: 0.5},
				{Key: "memory", Capacity: 32 << 30, Held: 12 << 30},
				{Key: "deploy-lock", Capacity: 1, Held: 1},
			},
			Holders: []Holder{
				{RunID: "r1", ElapsedMS: 42_000, Resources: HostResources{Cores: 4, MemoryBytes: 8 << 30}, Semaphores: []string{"deploy-lock"}},
				{RunID: "r2", ElapsedMS: 1_000, Resources: HostResources{Cores: 2.5, MemoryBytes: 4 << 30}},
			},
			Waiters: []Waiter{
				{RunID: "r3", Resources: HostResources{Cores: 1}},
				{RunID: "r4", Resources: HostResources{Cores: 8, MemoryBytes: 16 << 30}, Semaphores: []string{"db"}, BlockingReason: "needs 8.0 cores; 0.5 available (external load 1.5)"},
			},
		},
	}
}

func TestEncodeDecode_RoundTripsEveryMessageType(t *testing.T) {
	for _, msg := range roundTripMessages() {
		line, err := Encode(msg)
		if err != nil {
			t.Fatalf("Encode(%T): %v", msg, err)
		}
		got, err := Decode(line)
		if err != nil {
			t.Fatalf("Decode(%T): %v", msg, err)
		}
		if !reflect.DeepEqual(got, msg) {
			t.Errorf("%T round-trip mismatch:\n got %#v\nwant %#v", msg, got, msg)
		}
	}
}

func TestEncode_CoversEveryDeclaredType(t *testing.T) {
	seen := map[MessageType]bool{}
	for _, msg := range roundTripMessages() {
		line, err := Encode(msg)
		if err != nil {
			t.Fatalf("Encode(%T): %v", msg, err)
		}
		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("envelope(%T): %v", msg, err)
		}
		seen[env.Type] = true
	}
	all := []MessageType{
		TypeHello, TypeHelloAck, TypeAdmissionRequest, TypeGrant,
		TypeQueued, TypeEvicted, TypeRelease, TypeReattach,
		TypeDrainRequest, TypeDrainAck, TypeQueueState,
		TypeCancelLease, TypeCancelLeaseAck, TypeCancel,
	}
	for _, mt := range all {
		if !seen[mt] {
			t.Errorf("round-trip fixtures never exercise %q", mt)
		}
	}
}

func TestEncode_EmitsOneLine(t *testing.T) {
	for _, msg := range roundTripMessages() {
		line, err := Encode(msg)
		if err != nil {
			t.Fatalf("Encode(%T): %v", msg, err)
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			t.Errorf("%T: encoded message missing trailing newline", msg)
		}
		if bytes.Count(line, []byte("\n")) != 1 {
			t.Errorf("%T: encoded message spans multiple lines: %q", msg, line)
		}
	}
}

func TestEncode_EscapesNewlinesInsideStrings(t *testing.T) {
	line, err := Encode(&Queued{RunID: "run\nwith\nnewlines", Key: "cores", Position: 1, QueueLength: 1})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("string newlines leaked into framing: %q", line)
	}
	got, err := Decode(line)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.(*Queued).RunID != "run\nwith\nnewlines" {
		t.Errorf("RunID = %q, lost embedded newlines", got.(*Queued).RunID)
	}
}

func TestDecode_AcceptsLineWithoutTrailingNewline(t *testing.T) {
	line, err := Encode(&Release{LeaseToken: "lease-1"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(bytes.TrimSuffix(line, []byte("\n")))
	if err != nil {
		t.Fatalf("Decode without newline: %v", err)
	}
	if got.(*Release).LeaseToken != "lease-1" {
		t.Errorf("LeaseToken = %q, want lease-1", got.(*Release).LeaseToken)
	}
}

func TestDecode_RejectsUnknownType(t *testing.T) {
	_, err := Decode([]byte(`{"type":"warp_core_breach","payload":{}}`))
	if err == nil {
		t.Fatal("Decode accepted an unknown message type")
	}
	if !strings.Contains(err.Error(), "warp_core_breach") {
		t.Errorf("error does not name the unknown type: %v", err)
	}
}

func TestDecode_RejectsMalformedJSON(t *testing.T) {
	if _, err := Decode([]byte(`{"type":"hello","payload":`)); err == nil {
		t.Fatal("Decode accepted truncated JSON")
	}
	if _, err := Decode([]byte(`{"type":"hello","payload":"not-an-object"}`)); err == nil {
		t.Fatal("Decode accepted a payload of the wrong shape")
	}
}

func TestDecode_EmptyPayloadYieldsZeroMessage(t *testing.T) {
	got, err := Decode([]byte(`{"type":"drain_ack"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	ack, ok := got.(*DrainAck)
	if !ok {
		t.Fatalf("got %T, want *DrainAck", got)
	}
	if ack.HoldersRemaining != 0 {
		t.Errorf("HoldersRemaining = %d, want 0", ack.HoldersRemaining)
	}
}

func TestEncode_RejectsNilMessage(t *testing.T) {
	if _, err := Encode(nil); err == nil {
		t.Fatal("Encode accepted a nil message")
	}
}

// TestLeaseTokenEnv_IsStable pins the variable name: compiled pipeline
// binaries bake it in, so renaming it orphans every child spawned by
// an older parent.
func TestLeaseTokenEnv_IsStable(t *testing.T) {
	if LeaseTokenEnv != "SPARKWING_LEASE_TOKEN" {
		t.Fatalf("LeaseTokenEnv = %q", LeaseTokenEnv)
	}
	if ChildLeaseTokenEnv != "SPARKWING_CHILD_LEASE_TOKEN" {
		t.Fatalf("ChildLeaseTokenEnv = %q", ChildLeaseTokenEnv)
	}
}
