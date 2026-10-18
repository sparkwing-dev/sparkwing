package charts

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

var updateAdmission = flag.Bool("update", false, "rewrite the generated launcher admission template")

const (
	admissionTemplate = "sparkwing-runner-bundle/templates/launcher-admission.yaml"
	admissionHeader   = `{{- /* Generated from launcher.AdmissionPolicies: GOWORK=off go test ./charts -run TestLauncherAdmissionTemplateIsGenerated -update */}}
{{- if or .Values.launcher.enabled .Values.launcher.serviceAccountName }}
{{- $jobs := include "sparkwing-runner-bundle.jobNamespace" . }}
{{- $launcher := printf "system:serviceaccount:%s:%s" .Release.Namespace (include "sparkwing-runner-bundle.launcher.serviceAccountName" .) }}
{{- $runner := include "sparkwing-runner-bundle.digestImage" .Values.launcher.jobImage }}
{{- $docker := include "sparkwing-runner-bundle.digestImage" .Values.launcher.dockerImage }}
`
	testDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dockerDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func launcherImages(prefix string) []string {
	return []string{
		prefix + "launcher.jobImage.repository=registry.example/runner", prefix + "launcher.jobImage.tag=test", prefix + "launcher.jobImage.digest=" + testDigest,
		prefix + "launcher.dockerImage.repository=registry.example/docker", prefix + "launcher.dockerImage.tag=dind", prefix + "launcher.dockerImage.digest=" + dockerDigest,
	}
}

func generatedAdmissionTemplate(t *testing.T) []byte {
	t.Helper()
	policies, bindings := launcher.AdmissionPolicies(launcher.AdmissionConfig{Namespace: "{{ $jobs }}", Launcher: "{{ $launcher }}", RunnerImage: "{{ $runner }}", DockerImage: "{{ $docker }}"})
	out := bytes.NewBufferString(admissionHeader)
	for i := range policies {
		for _, object := range []any{policies[i], bindings[i]} {
			raw, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			delete(document, "status")
			delete(document["metadata"].(map[string]any), "creationTimestamp")
			body, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			out.WriteString("---\n")
			out.Write(body)
		}
	}
	out.WriteString("{{- end }}\n")
	return out.Bytes()
}

func TestLauncherAdmissionTemplateIsGenerated(t *testing.T) {
	want := generatedAdmissionTemplate(t)
	if *updateAdmission {
		if err := os.WriteFile(admissionTemplate, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(admissionTemplate)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; regenerate with: GOWORK=off go test ./charts -run TestLauncherAdmissionTemplateIsGenerated -update (%v)", admissionTemplate, err)
	}
}

func TestChartInstallsTheLaunchersAdmissionPolicies(t *testing.T) {
	for chart, prefix := range map[string]string{"./sparkwing-runner-bundle": "", "./sparkwing-full": "sparkwing-runner-bundle."} {
		for _, settings := range [][]string{
			append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher"),
			append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher", prefix+"launcher.jobNamespace=customer-jobs"),
		} {
			jobs := "cloud-jobs"
			if slices.Contains(settings, prefix+"launcher.jobNamespace=customer-jobs") {
				jobs = "customer-jobs"
			}
			policies, bindings := launcher.AdmissionPolicies(launcher.AdmissionConfig{Namespace: jobs, Launcher: "system:serviceaccount:cloud:cloud-launcher", RunnerImage: "registry.example/runner:test@" + testDigest, DockerImage: "registry.example/docker:dind@" + dockerDigest})
			found := 0
			for _, document := range strings.Split(helmRenderAll(t, chart, "sparkwing", "cloud", settings...), "\n---") {
				var kind struct{ Kind, APIVersion string }
				if err := yaml.Unmarshal([]byte(document), &kind); err != nil {
					t.Fatal(err)
				}
				switch kind.Kind {
				case "ValidatingAdmissionPolicy":
					var policy admissionv1.ValidatingAdmissionPolicy
					if err := yaml.Unmarshal([]byte(document), &policy); err != nil {
						t.Fatal(err)
					}
					i := slices.IndexFunc(policies, func(p *admissionv1.ValidatingAdmissionPolicy) bool { return p.Name == policy.Name })
					if i < 0 || kind.APIVersion != "admissionregistration.k8s.io/v1" || !reflect.DeepEqual(policy.Spec, policies[i].Spec) {
						t.Errorf("%s renders policy %s unlike the launcher's expectation for %s", chart, policy.Name, jobs)
					}
					found++
				case "ValidatingAdmissionPolicyBinding":
					var binding admissionv1.ValidatingAdmissionPolicyBinding
					if err := yaml.Unmarshal([]byte(document), &binding); err != nil {
						t.Fatal(err)
					}
					i := slices.IndexFunc(bindings, func(b *admissionv1.ValidatingAdmissionPolicyBinding) bool { return b.Name == binding.Name })
					if i < 0 || !reflect.DeepEqual(binding.Spec, bindings[i].Spec) {
						t.Errorf("%s renders binding %s unlike the launcher's expectation for %s", chart, binding.Name, jobs)
					}
					found++
				}
			}
			if found != 4 {
				t.Errorf("%s rendered %d of the 4 admission objects", chart, found)
			}
		}
	}
}

func TestChartOmitsAdmissionPoliciesWithoutALauncher(t *testing.T) {
	for _, chart := range []string{"./sparkwing-runner-bundle", "./sparkwing-full"} {
		for _, r := range renderedResources(t, helmRenderAll(t, chart, "sparkwing", "cloud")) {
			if strings.HasPrefix(r.Kind, "ValidatingAdmissionPolicy") || r.Kind == "Namespace" {
				t.Errorf("%s renders %s without a launcher", chart, r.Kind)
			}
		}
	}
}

func TestJobNamespaceMustBeDedicated(t *testing.T) {
	settings := append(launcherImages(""), "launcher.serviceAccountName=cloud-launcher", "launcher.jobNamespace=default")
	if out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", settings...); !strings.Contains(out, "launcher.jobNamespace must differ from the release namespace") {
		t.Fatalf("shared job namespace render error = %s", out)
	}
}
