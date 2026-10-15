package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func transportAward(t *testing.T) (CloudJobAward, CloudJobResources) {
	t.Helper()
	launcher, _, boundary, control := fixture()
	award := *control.award
	award.Source = source.Spec{RepoKey: "github.com/team/repo", RemoteURL: "https://github.com/team/repo.git", SHA: strings.Repeat("0123456789", 4), PipelineDir: ".sparkwing", Engine: award.Engine, BundleDigest: strings.Repeat("b", 64)}
	resources, err := BuildJob(t.Context(), time.Now(), launcher.Config, award, boundary)
	if err != nil {
		t.Fatal(err)
	}
	return award, resources
}

func TestCloudAwardTransportPreservesPrivateInputs(t *testing.T) {
	award, resources := transportAward(t)
	metadata := resources.BootstrapSecret.Data[awardFileName]
	if len(metadata) == 0 || bytes.Contains(metadata, award.Bootstrap) {
		t.Fatal("award metadata is absent or includes the bootstrap")
	}
	got, err := decodeBootstrapAward(metadata, resources.BootstrapSecret.Data["bootstrap"])
	if err != nil || !reflect.DeepEqual(got, award) {
		t.Fatalf("award roundtrip changed its source, engine or execution inputs: %v", err)
	}
	pod, err := json.Marshal(resources.Job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pod, award.Bootstrap) || bytes.Contains(pod, []byte(award.Source.SHA)) || bytes.Contains(pod, []byte(award.Source.RemoteURL)) {
		t.Fatal("private transport leaked into the Kubernetes Job")
	}
}

func TestCloudAwardTransportRefusesInvalidMetadata(t *testing.T) {
	award, resources := transportAward(t)
	metadata := string(resources.BootstrapSecret.Data[awardFileName])
	for name, body := range map[string]string{
		"missing": "", "null": "null", "array": "[]", "trailing": metadata + metadata,
		"unknown":          strings.Replace(metadata, "{", `{"unexpected":"bootstrap-secret",`, 1),
		"credential":       strings.Replace(metadata, "{", `{"Bootstrap":"bootstrap-secret",`, 1),
		"duplicate":        strings.Replace(metadata, "{", `{"cpu_cores":1,`, 1),
		"nested_duplicate": strings.Replace(metadata, `"UnitID":`, `"UnitID":"other","UnitID":`, 1),
		"case":             strings.Replace(metadata, `"cpu_cores":`, `"CPU_CORES":`, 1),
		"invalid_award":    strings.Replace(metadata, `"cpu_cores":1.5`, `"cpu_cores":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeBootstrapAward([]byte(body), award.Bootstrap)
			if err == nil || !reflect.DeepEqual(got, CloudJobAward{}) || strings.Contains(err.Error(), string(award.Bootstrap)) {
				t.Fatal("invalid metadata returned usable authority or disclosed the bootstrap")
			}
		})
	}
	if got, err := decodeBootstrapAward([]byte(metadata), nil); err == nil || !reflect.DeepEqual(got, CloudJobAward{}) {
		t.Fatal("missing bootstrap returned an award")
	}
}

func TestReadBootstrapAwardRequiresBothFiles(t *testing.T) {
	_, resources := transportAward(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "bootstrap")
	if _, err := ReadBootstrapAward(path); err == nil {
		t.Fatal("missing Secret files accepted")
	}
	if err := os.WriteFile(path, resources.BootstrapSecret.Data["bootstrap"], 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBootstrapAward(path); err == nil {
		t.Fatal("missing award metadata accepted")
	}
	if _, err := ReadBootstrapAward("bootstrap"); err == nil {
		t.Fatal("relative bootstrap path accepted")
	}
	if _, err := ReadBootstrapAward(filepath.Join(directory, awardFileName)); err == nil {
		t.Fatal("award metadata used as a bootstrap token")
	}
}

func TestCloudAwardTransportRequiresSourceMetadata(t *testing.T) {
	award, resources := transportAward(t)
	for name, value := range map[string]json.RawMessage{"absent": nil, "null": json.RawMessage("null")} {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(resources.BootstrapSecret.Data[awardFileName], &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "source")
			if value != nil {
				fields["source"] = value
			}
			body, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeBootstrapAward(body, award.Bootstrap)
			if err == nil || !reflect.DeepEqual(got, CloudJobAward{}) {
				t.Fatal("missing source metadata returned usable bootstrap authority")
			}
		})
	}
}

func TestBootstrapFileCancellationPrecedesAcquisition(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	failure := errors.New("host stopped")
	cancel(failure)
	if err := (BootstrapHost{}).RunFile(ctx, filepath.Join(t.TempDir(), "absent")); !errors.Is(err, failure) {
		t.Fatal("cancelled host attempted private file acquisition")
	}
}

func FuzzCloudAwardTransport(f *testing.F) {
	f.Add("source", "holder", int64(0), uint8(0))
	f.Add("quoted\"\\\n", "other", int64(42), uint8(1))
	f.Fuzz(func(t *testing.T, sourceSHA, holder string, attempt int64, mutation uint8) {
		award, resources := transportAward(t)
		award.Source.SHA, award.Binding.Holder, award.Binding.Attempt = sourceSHA, holder, attempt
		metadata, err := json.Marshal(award)
		if err != nil {
			t.Fatal(err)
		}
		if mutation%2 != 0 {
			metadata = append([]byte(`{"bootstrap-secret":null,`), metadata[1:]...)
		}
		valid := mutation%2 == 0 && holder != "" && attempt >= 0
		got, err := decodeBootstrapAward(metadata, resources.BootstrapSecret.Data["bootstrap"])
		if (err == nil) != valid {
			t.Fatal("private award validation disagreed with its execution fence")
		}
		if err != nil {
			if !reflect.DeepEqual(got, CloudJobAward{}) || strings.Contains(err.Error(), string(award.Bootstrap)) {
				t.Fatal("invalid private metadata disclosed bootstrap authority")
			}
		} else {
			var canonical CloudJobAward
			if err := json.Unmarshal(metadata, &canonical); err != nil {
				t.Fatal(err)
			}
			canonical.Bootstrap = award.Bootstrap
			if !reflect.DeepEqual(got, canonical) {
				t.Fatal("private award transport changed execution inputs")
			}
		}
	})
}
