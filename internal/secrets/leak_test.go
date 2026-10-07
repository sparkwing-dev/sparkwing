package secrets_test

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(maskFixtureEnv); mode != "" {
		runMaskFixture(mode)
		return
	}
	testleak.Main(m)
}
