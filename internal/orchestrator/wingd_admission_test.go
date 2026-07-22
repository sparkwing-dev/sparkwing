package orchestrator

import (
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

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
