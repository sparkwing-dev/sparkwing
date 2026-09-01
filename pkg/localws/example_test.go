package localws_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/localws"
)

func ExampleRun() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = func() error {
		return localws.Run(ctx, localws.Options{
			Addr: "127.0.0.1:4343",
			Home: "/tmp/sparkwing-home",
		})
	}
}
