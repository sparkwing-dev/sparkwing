package nodemetrics

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestAttachRecoversWithoutBridgingMissingReadings(t *testing.T) {
	start := time.Unix(100, 0)
	for _, tc := range []struct {
		name      string
		available []bool
		want      []bool
	}{
		{"initial failure", []bool{false, true, true}, []bool{false, false, true}},
		{"interrupted", []bool{true, true, false, true, true}, []bool{true, false, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var readings []reading
				for i, ok := range tc.available {
					readings = append(readings, reading{start.Add(time.Duration(i) * time.Second), time.Duration(i) * time.Second, 100, ok, nil})
				}
				fixedReadings(t, readings...)
				t.Cleanup(SetIntervalForTest(time.Millisecond))
				samples := make(chan Sample, 32)
				finish := Attach(t.Context(), sinkFunc(func(_ context.Context, s Sample) error {
					select {
					case samples <- s:
					default:
					}
					return nil
				}))
				defer func() {
					if err := finish(); err != nil {
						t.Error(err)
					}
				}()
				for i, want := range tc.want {
					select {
					case got := <-samples:
						if got.Valid != want || (got.CPUMillicores > 0) != want {
							t.Errorf("sample%d=%+v; want valid/positive=%t", i, got, want)
						}
					case <-time.After(time.Second):
						t.Fatal("sample deadline")
					}
				}
			})
		})
	}
}
