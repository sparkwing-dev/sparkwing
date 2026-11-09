package sdkchecks

import (
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testleak"
	"testing"
)

func TestMain(m *testing.M) { testleak.Main(m) }
