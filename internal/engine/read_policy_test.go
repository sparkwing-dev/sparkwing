package engine

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestPolicyMetadataReadsVerifyWithoutCreatingState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("SPARKWING_HOME", home)
	reader, err := newPolicyReader()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("policy reader created state: %v", err)
	}
	key := testSigningKey(t)
	policy, now := policyFixture()
	transport := &fixtureTransport{responses: map[string][]byte{}}
	transport.policy(t, key, policy)
	reader.config.TrustedKeys = signingKeys(key)
	reader.config.HTTPClient = &http.Client{Transport: transport}
	result, err := reader.indexPolicy(t.Context(), now, false)
	if err != nil {
		t.Fatal(err)
	}
	sdk, _, err := SelectSDK(result.Index, engineid.GoSDK, "", now)
	if err != nil || sdk.Version != "v0.20.0" {
		t.Fatalf("signed metadata: %+v %v", sdk, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("metadata lookup persisted state: %v", err)
	}
	if _, err := CachedEngineVersion(t.Context(), engineid.Pin{SDK: engineid.GoSDK, Version: "v0.20.0"}); err == nil {
		t.Fatal("missing cache supplied engine authority")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("cached metadata lookup created state: %v", err)
	}
}
