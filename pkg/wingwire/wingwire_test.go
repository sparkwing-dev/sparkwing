package wingwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func roundTripMessages() []any {
	return []any{
		&HelloAck{ProtocolMajor: ProtocolMajor, BinaryVersion: "v0.16.0", Draining: true},
		&AdmissionRequest{
			RunID:     "deploy-20260710-120000",
			Resources: HostResources{Cores: 2.5, MemoryBytes: 4 << 30},
			Semaphores: []SemaphoreClaim{
				{Name: "deploy-lock", Cost: 1, Capacity: 1, Policy: PolicyQueue, QueueTimeoutMS: 60_000},
				{Name: "db", Cost: 4, Capacity: 8, Policy: PolicyCancelOthers},
			},
		},
		&Grant{
			RunID:      "deploy-20260710-120000",
			LeaseToken: "lease-def456",
			Resources:  HostResources{Cores: 2.5, MemoryBytes: 4 << 30},
		},
		&Queued{RunID: "r1", Key: "cores", Position: 2, QueueLength: 3},
		&Evicted{RunID: "r1", Key: "deploy-lock", SupersededBy: "r2", Policy: PolicyCancelOthers},
		&DrainAck{HoldersRemaining: 3},
		&CancelLease{RunID: "deploy-20260710-120000"},
		&CancelLeaseAck{Found: true},
		&SetPriority{RunID: "deploy-20260710-120000", Priority: 7},
		&SetPriorityAck{Found: true, Previous: 0, Priority: 7, Position: 1, Participants: 3},
		&StatsReset{},
		&StatsResetAck{},
		&LocalControl{},
		&LocalDashboard{Address: "127.0.0.1:0"},
		&LocalDashboardAck{},
		&LocalDashboardAck{Error: "dashboard address is occupied"},
		&LocalControlStatus{Running: true, PID: 91, Version: "v1", Runs: 1, QueuedTriggers: 2, HeldLeases: 3, DashboardConnections: 4},
		&Cancel{RunID: "deploy-20260710-120000", Reason: "cancelled via sparkwing runs cancel"},
		&QueueState{
			ExternalSampleAgeMS:      30000,
			ExternalMeasurementAgeMS: 2000,
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

func TestQueueStateOmitsAbsentMeasurementAge(t *testing.T) {
	body, err := json.Marshal(&QueueState{ExternalSampleAgeMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "external_measurement_age_ms") {
		t.Fatalf("zero measurement age was not omitted: %s", body)
	}
}

func TestControllerJSONRoundTrips(t *testing.T) {
	for _, value := range roundTripMessages() {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		got := reflect.New(reflect.TypeOf(value).Elem()).Interface()
		if err := json.Unmarshal(body, got); err != nil {
			t.Fatalf("unmarshal %T: %v", value, err)
		}
		if !reflect.DeepEqual(got, value) {
			t.Errorf("%T round trip = %#v, want %#v", value, got, value)
		}
	}
}
