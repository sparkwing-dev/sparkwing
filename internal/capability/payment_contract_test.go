package capability

import (
	"encoding/json"
	"os"
	"testing"
)

// The Stripe metadata keys the controller settles by match this repository's copy of the checkout contract. The
// checkout service tests against its own copy, which nothing compares with this one, so renaming a key updates both.
func TestPaymentMetadataMatchesTheCheckoutContract(t *testing.T) {
	data, err := os.ReadFile("../controller/testdata/checkout_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"deployment": paymentDeploymentMetadata, "purpose": paymentPurposeMetadata, "refill_id": paymentRefillMetadata,
		"card_setup": paymentPurposeSetup, "card_refill": paymentPurposeRefill,
	}
	if len(contract.Metadata) != len(want) {
		t.Fatalf("contract metadata %v; want %v", contract.Metadata, want)
	}
	for name, value := range want {
		if contract.Metadata[name] != value {
			t.Errorf("metadata %s: contract %q, controller %q", name, contract.Metadata[name], value)
		}
	}
}
