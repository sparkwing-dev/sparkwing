package store_test

import (
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"testing"
)

func TestValidateRawToken_AcceptsAMintedShape(t *testing.T) {
	if err := store.ValidateRawToken("swu_bootstrapadmintokenvalue000001", store.TokenKindUser); err != nil {
		t.Fatalf("ValidateRawToken: %v", err)
	}
	if err := store.ValidateRawToken("swu_bootstrapadmintokenvalue000001", "nonesuch"); err == nil {
		t.Fatal("ValidateRawToken accepted an unknown kind")
	}
}
