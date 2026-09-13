package backends

import (
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestLayerSurfaces_KeepsBaseLogBatchKnobs(t *testing.T) {
	base := Surfaces{Logs: &Spec{
		Type:          TypeS3,
		Bucket:        "team",
		BatchInterval: 5 * time.Second,
		BatchBytes:    4096,
		MaxLogObjects: 7,
		MaxLogBytes:   1 << 20,
	}}
	over := Surfaces{Logs: &Spec{Type: TypeS3, Prefix: "logs"}}

	got := LayerSurfaces(base, over).Logs
	if got.BatchInterval != 5*time.Second {
		t.Errorf("batch_interval = %v, want 5s", got.BatchInterval)
	}
	if got.BatchBytes != 4096 {
		t.Errorf("batch_bytes = %d, want 4096", got.BatchBytes)
	}
	if got.MaxLogObjects != 7 {
		t.Errorf("max_log_objects = %d, want 7", got.MaxLogObjects)
	}
	if got.MaxLogBytes != 1<<20 {
		t.Errorf("max_log_bytes = %d, want %d", got.MaxLogBytes, 1<<20)
	}
	if got.Prefix != "logs" {
		t.Errorf("prefix = %q, want the override", got.Prefix)
	}
}

func TestSpec_LogBatchKnobsDecodeFromYAML(t *testing.T) {
	var s Spec
	if err := yaml.Unmarshal([]byte("type: s3\nbucket: team\nbatch_interval: 3s\nbatch_bytes: 1024\nmax_log_objects: 9\nmax_log_bytes: 2048\n"), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.BatchInterval != 3*time.Second {
		t.Errorf("batch_interval = %v, want 3s", s.BatchInterval)
	}
	if s.BatchBytes != 1024 || s.MaxLogObjects != 9 || s.MaxLogBytes != 2048 {
		t.Errorf("caps decoded as %d/%d/%d", s.BatchBytes, s.MaxLogObjects, s.MaxLogBytes)
	}
}
