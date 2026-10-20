package nodehost

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	runAsFakeProcess()
	testleak.Main(m)
}
