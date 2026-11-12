// Package rsatest leases runtime-generated RSA keys to test identity fixtures.
package rsatest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"sync"
	"testing"
)

type keyPool struct {
	mu   sync.Mutex
	free [][]byte
}

var processKeys keyPool

func (p *keyPool) take() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.free); n > 0 {
		der := p.free[n-1]
		p.free[n-1] = nil
		p.free = p.free[:n-1]
		return der, nil
	}
	// perf: hold admission through generation so only the live-lease peak creates keys.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return x509.MarshalPKCS1PrivateKey(key), nil
}

func (p *keyPool) put(der []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.free = append(p.free, der)
}

// New leases a 2048-bit key exclusively until t's cleanup. Register server
// cleanup after New so the server closes before the key returns to the pool.
// Later fixtures can reuse key material; each lease owns its parsed key object.
func New(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	der, err := processKeys.take()
	if err != nil {
		t.Fatalf("rsatest: generate key: %v", err)
	}
	t.Cleanup(func() { processKeys.put(der) })
	// safety: mutable private-key objects must not carry state into a later fixture.
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		t.Fatalf("rsatest: parse generated key: %v", err)
	}
	return key
}
