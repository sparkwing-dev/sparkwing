package backends

import (
	"strings"
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

func TestValidateFields_RejectsNegativeLogBatchKnobs(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec Spec
		want string
	}{
		{"interval", Spec{Type: TypeS3, Bucket: "b", BatchInterval: -time.Second}, "batch_interval must be positive"},
		{"bytes", Spec{Type: TypeS3, Bucket: "b", BatchBytes: -1}, "batch_bytes must be positive"},
		{"objects", Spec{Type: TypeS3, Bucket: "b", MaxLogObjects: -1}, "max_log_objects must be positive"},
		{"log bytes", Spec{Type: TypeS3, Bucket: "b", MaxLogBytes: -1}, "max_log_bytes must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.ValidateFields("logs")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateFields = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

func TestValidateFields_RejectsLogBatchKnobsWhereTheyDoNothing(t *testing.T) {
	fs := Spec{Type: TypeFilesystem, Path: "/tmp/logs", BatchBytes: 4096}
	if err := fs.ValidateFields("logs"); err == nil || !strings.Contains(err.Error(), "object-store logs surface") {
		t.Fatalf("filesystem logs surface with batch_bytes = %v, want a refusal naming the object-store requirement", err)
	}
	cache := Spec{Type: TypeS3, Bucket: "b", BatchBytes: 4096}
	if err := cache.ValidateFields("cache"); err == nil || !strings.Contains(err.Error(), "only to the logs surface") {
		t.Fatalf("cache surface with batch_bytes = %v, want a refusal naming the logs surface", err)
	}
}

func TestValidateFields_AcceptsLogBatchKnobsOnAnObjectStore(t *testing.T) {
	spec := Spec{
		Type: TypeS3, Bucket: "b",
		BatchInterval: 5 * time.Second, BatchBytes: 4096,
		MaxLogObjects: 7, MaxLogBytes: 1 << 20,
	}
	if err := spec.ValidateFields("logs"); err != nil {
		t.Fatalf("ValidateFields = %v, want nil", err)
	}
}
