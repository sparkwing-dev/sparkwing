package store

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// The deployment identity a store mints has the shape the checkout contract carries, so the checkout service's tests,
// which check that shape against BILLING_DEPLOYMENT's rule, judge the value a real controller sends.
func TestMintedPaymentDeploymentHasTheCheckoutContractShape(t *testing.T) {
	data, err := os.ReadFile("../controller/testdata/checkout_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Deployment string `json:"deployment"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	shape := regexp.MustCompile(`^dep-[0-9a-f]{24}$`)
	var minted string
	s := authorityStore(t)
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		minted, err = deploymentIDTx(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !shape.MatchString(minted) || !shape.MatchString(contract.Deployment) {
		t.Fatalf("minted %q, contract %q; want both %s", minted, contract.Deployment, shape)
	}
}
