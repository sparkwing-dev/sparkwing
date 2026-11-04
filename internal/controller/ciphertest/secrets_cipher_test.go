package ciphertest_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/ciphertest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// perf: these checks live outside internal/secrets because `go mod tidy` in a pipeline loads that package's
// tests, and a secrets test importing the controller downloaded the controller's dependencies.

var _ controller.Cipher = (*secrets.Cipher)(nil)

var _ controller.BoundCipher = (*secrets.Cipher)(nil)

func TestConformance_Cipher(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ciphertest.TestCipher(t, func() controller.Cipher {
		c, err := secrets.NewCipher(key)
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		return c
	})
}

func TestConformance_BoundCipher(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ciphertest.TestBoundCipher(t, func() controller.BoundCipher {
		c, err := secrets.NewCipher(key)
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		return c
	})
}
