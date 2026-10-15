package orchestrator

import "testing"

func TestProtocolTriggerSourceNamesEveryRecordedSource(t *testing.T) {
	for recorded, want := range map[string]string{
		"": "manual", "manual": "manual", "cli": "manual", "api": "manual", "runs-submit": "manual",
		"retry": "manual", "await-pipeline": "manual", "pipeline-working-tree@laptop": "manual",
		"schedule": "schedule", "push": "push", "webhook": "webhook", "github": "webhook",
	} {
		if got := protocolTriggerSource(recorded); got != want {
			t.Errorf("protocolTriggerSource(%q) = %q, want %q", recorded, got, want)
		}
	}
}
