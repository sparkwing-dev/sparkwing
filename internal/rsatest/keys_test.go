package rsatest

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"sync"
	"testing"
)

type owner struct {
	testing.TB
	name     string
	cleanups []func()
}

func TestKeysKeepConcurrentRootsDistinct(t *testing.T) {
	warm := &owner{TB: t, name: t.Name() + "Warm"}
	New(warm)
	warm.finish()
	a := &owner{TB: t, name: t.Name() + "A"}
	defer a.finish()
	b := &owner{TB: t, name: t.Name() + "B"}
	defer b.finish()
	if New(a).Equal(New(b)) {
		t.Fatal("concurrent root fixtures shared key material")
	}
}

func TestKeysKeepParentsAndParallelChildrenDistinct(t *testing.T) {
	parent := New(t)
	var seen sync.Map
	seen.Store(parent.N.String(), true)
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			key := New(t)
			if _, loaded := seen.LoadOrStore(key.N.String(), true); loaded {
				t.Fatal("live fixtures shared key material")
			}
			t.Cleanup(func() { seen.Delete(key.N.String()) })
		})
	}
}

func TestKeysReleaseChildLeaseWithoutReleasingParent(t *testing.T) {
	parent := &owner{TB: t, name: t.Name()}
	defer parent.finish()
	parentKey := New(parent)
	child := &owner{TB: t, name: t.Name() + "/child"}
	childKey := New(child)
	child.finish()
	next := &owner{TB: t, name: t.Name() + "/next"}
	defer next.finish()
	nextKey := New(next)
	if !childKey.Equal(nextKey) || parentKey.Equal(nextKey) {
		t.Fatal("child cleanup must release only the child's lease")
	}
}

func TestKeysDoNotRetainMutableStateAfterRelease(t *testing.T) {
	first := &owner{TB: t, name: t.Name() + "First"}
	key := New(first)
	wantN := key.N.String()
	key.N.SetInt64(1)
	key.D.SetInt64(1)
	key.Primes[0].SetInt64(1)
	first.finish()
	second := &owner{TB: t, name: t.Name() + "Second"}
	defer second.finish()
	reused := New(second)
	if reused.N.String() != wantN || reused.Validate() != nil {
		t.Fatal("mutable key state escaped its fixture")
	}
}

func TestKeyPoolGeneratesOnlyForPeakLiveLeases(t *testing.T) {
	var pool keyPool
	take := func() []byte {
		t.Helper()
		der, err := pool.take()
		if err != nil {
			t.Fatal(err)
		}
		key, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil || key.N.BitLen() != 2048 {
			t.Fatalf("key size or encoding changed: %v", err)
		}
		return der
	}
	first, second := take(), take()
	if bytes.Equal(first, second) {
		t.Fatal("simultaneous leases shared a key")
	}
	pool.put(first)
	reused := take()
	if !bytes.Equal(first, reused) {
		t.Fatal("free key was not reused")
	}
	pool.put(reused)
	pool.put(second)
	for range 20 {
		der := take()
		if !bytes.Equal(der, first) && !bytes.Equal(der, second) {
			t.Fatal("generated beyond the two-key live peak")
		}
		pool.put(der)
	}
	if len(pool.free) != 2 {
		t.Fatalf("retained %d keys for a peak of two", len(pool.free))
	}
}
func (o *owner) Name() string     { return o.name }
func (o *owner) Cleanup(f func()) { o.cleanups = append(o.cleanups, f) }
func (o *owner) finish() {
	for i := len(o.cleanups) - 1; i >= 0; i-- {
		o.cleanups[i]()
	}
	o.cleanups = nil
}

func TestKeysReuseMaterialAfterOwnerCleanup(t *testing.T) {
	a := &owner{TB: t, name: t.Name() + "/first"}
	first := New(a)
	a.finish()
	b := &owner{TB: t, name: t.Name() + "/second"}
	defer b.finish()
	second := New(b)
	if !first.Equal(second) {
		t.Fatal("finished fixtures must reuse released key material rather than generate fresh RSA keys")
	}
	if first == second || first.N == second.N || first.D == second.D {
		t.Fatal("fixtures must own independent mutable key objects")
	}
}
