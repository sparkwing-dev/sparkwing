package secrets_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/ciphertest"
)

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
