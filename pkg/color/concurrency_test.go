package color_test

import (
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func TestConcurrentColorOverrideAndOutput(t *testing.T) {
	before := color.Enabled()
	defer color.SetEnabled(before)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			color.SetEnabled(i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			color.Bold("output")
			color.Enabled()
		}
	}()
	wg.Wait()
}
