package storeurl_test

import (
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestMain(m *testing.M) {
	var opts []goleak.Option
	if os.Getenv("SPARKWING_S3_TEST_BUCKET") != "" {
		// safety: the SDK exposes no close operation for its keep-alive pool.
		opts = append(opts,
			goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
			goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		)
	}
	testleak.Main(m, opts...)
}
