// Package ciphertest ships a portable conformance suite for the
// [github.com/sparkwing-dev/sparkwing/pkg/controller.Cipher]
// interface. External implementations (KMS-backed, HSM-backed,
// any custom AEAD) call the suite from their own *_test.go to prove
// they honor the contract:
//
//	import "github.com/sparkwing-dev/sparkwing/pkg/controller/ciphertest"
//
//	func TestConformance(t *testing.T) {
//	    ciphertest.TestCipher(t, func() controller.Cipher {
//	        c, _ := mybackend.New(testKey)
//	        return c
//	    })
//	}
//
// An implementation that also satisfies
// [github.com/sparkwing-dev/sparkwing/pkg/controller.BoundCipher]
// calls TestBoundCipher the same way to prove an envelope sealed for
// one row does not open for another.
//
// The factory returns a fresh cipher per subtest -- the suite
// assumes isolation (a Seal in one subtest shouldn't affect Open in
// another).
package ciphertest
