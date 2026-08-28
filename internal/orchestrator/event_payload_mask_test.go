package orchestrator

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestMaskEventPayload_RedactsForwardedSecrets(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("s3cr3t-token-value")

	payload := []byte(`{"child_run_id":"c1","args":{"token":"s3cr3t-token-value","env":"prod"}}`)
	got := string(maskEventPayload(m, payload))

	if strings.Contains(got, "s3cr3t-token-value") {
		t.Errorf("payload still carries the secret: %s", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("payload carries no redaction marker: %s", got)
	}
	if !strings.Contains(got, `"env":"prod"`) {
		t.Errorf("non-secret content was damaged: %s", got)
	}
}

func TestMaskEventPayload_CoversEmbeddedSecrets(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("s3cr3t")
	got := string(maskEventPayload(m, []byte(`{"args":{"url":"https://x/?t=s3cr3t"}}`)))
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("embedded secret survived: %s", got)
	}
}

func TestMaskEventPayload_NoOpWithoutSecrets(t *testing.T) {
	payload := []byte(`{"child_run_id":"c1","args":{"env":"prod"}}`)
	if got := maskEventPayload(secrets.NewMasker(), payload); string(got) != string(payload) {
		t.Errorf("payload changed: %s", got)
	}
	if got := maskEventPayload(nil, payload); string(got) != string(payload) {
		t.Errorf("nil masker changed the payload: %s", got)
	}
}
