package orchestrator

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseK8sNodeSelector(t *testing.T) {
	got, err := parseK8sNodeSelector([]string{
		"sparkwing.io/node-pool=runner",
		"kubernetes.io/arch=arm64",
	})
	if err != nil {
		t.Fatalf("parseK8sNodeSelector() error = %v", err)
	}
	want := map[string]string{
		"sparkwing.io/node-pool": "runner",
		"kubernetes.io/arch":     "arm64",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseK8sNodeSelector() = %#v, want %#v", got, want)
	}
}

func TestParseK8sTolerations(t *testing.T) {
	got, err := parseK8sTolerations([]string{
		"sparkwing.io/node-pool=runner:NoSchedule",
		"dedicated:NoExecute",
	})
	if err != nil {
		t.Fatalf("parseK8sTolerations() error = %v", err)
	}
	want := []corev1.Toleration{
		{
			Key:      "sparkwing.io/node-pool",
			Operator: corev1.TolerationOpEqual,
			Value:    "runner",
			Effect:   corev1.TaintEffectNoSchedule,
		},
		{
			Key:      "dedicated",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoExecute,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseK8sTolerations() = %#v, want %#v", got, want)
	}
}

func TestParseK8sPlacementRejectsInvalidValues(t *testing.T) {
	if _, err := parseK8sNodeSelector([]string{"missing-value"}); err == nil {
		t.Fatal("parseK8sNodeSelector() error = nil, want error")
	}
	if _, err := parseK8sTolerations([]string{"missing-effect"}); err == nil {
		t.Fatal("parseK8sTolerations() error = nil, want error")
	}
}
