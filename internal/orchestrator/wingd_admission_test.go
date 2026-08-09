package orchestrator

import (
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestAdmissionEventNodeIDUsesCanonicalParticipant(t *testing.T) {
	for _, tc := range []struct {
		name, participant, requestID, want string
	}{
		{name: "root", requestID: "run-1", want: ""},
		{name: "plan", requestID: "run-1/plan", want: ""},
		{name: "node", participant: "compile", requestID: "run-1/node-host/Y29tcGlsZQ", want: "compile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admissionEventNodeID(tc.participant, tc.requestID); got != tc.want {
				t.Fatalf("admissionEventNodeID(%q, %q) = %q, want %q", tc.participant, tc.requestID, got, tc.want)
			}
		})
	}
}

func TestLeaseCarriesHost(t *testing.T) {
	tests := []struct {
		name  string
		lease *wingdclient.Lease
		want  bool
	}{
		{name: "nil", lease: nil, want: false},
		{name: "zero", lease: &wingdclient.Lease{}, want: false},
		{name: "cores", lease: &wingdclient.Lease{Resources: wingwire.HostResources{Cores: 0.1}}, want: true},
		{name: "memory", lease: &wingdclient.Lease{Resources: wingwire.HostResources{MemoryBytes: 1}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leaseCarriesHost(tt.lease); got != tt.want {
				t.Fatalf("leaseCarriesHost() = %v, want %v", got, tt.want)
			}
		})
	}
}
