package secrets

import (
	"encoding/base64"
	"math/rand"
	"runtime"
	"testing"
)

func fakePEMKeys() []string {
	random := rand.New(rand.NewSource(43150))
	values := make([]string, 50)
	for i := range values {
		data := make([]byte, 1152)
		if _, err := random.Read(data); err != nil {
			panic(err)
		}
		values[i] = "-----BEGIN PRIVATE KEY-----\n" + base64.StdEncoding.EncodeToString(data) + "\n-----END PRIVATE KEY-----\n"
	}
	return values
}

func BenchmarkMaskRegistry50PEM(b *testing.B) {
	keys := fakePEMKeys()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m := NewMasker()
		for _, key := range keys {
			if err := m.Register(key); err != nil {
				b.Fatal(err)
			}
			m.matcher()
		}
		runtime.KeepAlive(m)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(keys)), "ns/key")
}

func BenchmarkMaskRegistry50PEMRetained(b *testing.B) {
	keys := fakePEMKeys()
	for range b.N {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		m := NewMasker()
		for _, key := range keys {
			if err := m.Register(key); err != nil {
				b.Fatal(err)
			}
			m.matcher()
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "retained-B")
		runtime.KeepAlive(m)
	}
}
