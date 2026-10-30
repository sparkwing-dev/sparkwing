package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestKubernetesE2EChartRender(t *testing.T) {
	for _, tool := range []string{"bash", "helm", "jq", "openssl", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable: %v", tool, err)
		}
	}
	cmd := exec.Command("bash", "k8s-e2e-render-test.sh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("offline Kubernetes harness: %v\n%s", err, output)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	counts := map[string]int{}
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Args []string `yaml:"args"`
						} `yaml:"containers"`
						Volumes []struct {
							Projected struct {
								Sources []struct {
									Secret struct {
										Name string `yaml:"name"`
									} `yaml:"secret"`
								} `yaml:"sources"`
							} `yaml:"projected"`
						} `yaml:"volumes"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc.Kind != "Deployment" {
			continue
		}
		component := doc.Metadata.Labels["app.kubernetes.io/component"]
		counts[component]++
		switch component {
		case "controller":
			args := doc.Spec.Template.Spec.Containers[0].Args
			if !slices.Contains(args, "--deployment=team") || !slices.Contains(args, "--require-auth") || slices.Contains(args, "--insecure-cookies") {
				t.Fatalf("controller args: %v", args)
			}
			secrets := []string{}
			for _, volume := range doc.Spec.Template.Spec.Volumes {
				for _, source := range volume.Projected.Sources {
					secrets = append(secrets, source.Secret.Name)
				}
			}
			for _, name := range []string{"sparkwing-bootstrap-token", "sparkwing-e2e-credentials", "sparkwing-secrets-key"} {
				if !slices.Contains(secrets, name) {
					t.Errorf("controller is missing credential secret %s", name)
				}
			}
		case "runner":
		default:
			t.Errorf("unexpected deployment component %q", component)
		}
	}
	if counts["controller"] != 2 || counts["runner"] != 1 {
		t.Fatalf("bootstrap and authenticated deployment counts: %v", counts)
	}
}

func TestKubernetesE2EFixtureBuildsWithItsPinnedSDK(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("Go unavailable: %v", err)
	}
	cmd := exec.Command("go", "test", "-mod=readonly", "./...")
	cmd.Dir = "../testdata/k8s-e2e/repo/.sparkwing"
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false", "GOPROXY=off", "GOSUMDB=off")
	output, err := cmd.CombinedOutput()
	if bytes.Contains(output, []byte("module lookup disabled by GOPROXY=off")) {
		t.Skipf("pinned fixture dependencies are not cached: %s", output)
	}
	if err != nil {
		t.Fatalf("pinned Kubernetes fixture: %v\n%s", err, output)
	}
}
