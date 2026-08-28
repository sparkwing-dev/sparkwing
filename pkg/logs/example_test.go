package logs_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func ExampleClient() {
	dir, _ := os.MkdirTemp("", "sparkwing-logs-")
	defer os.RemoveAll(dir)

	srv, err := logs.New(dir, nil)
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx := context.Background()
	c := logs.NewClient(ts.URL, nil)
	if err := c.Append(ctx, "run-1", "build", []byte("compiling...\n")); err != nil {
		fmt.Println("append:", err)
		return
	}
	if err := c.Append(ctx, "run-1", "build", []byte("ok\n")); err != nil {
		fmt.Println("append:", err)
		return
	}

	body, err := c.Read(ctx, "run-1", "build")
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Print(strings.TrimRight(string(body), "\n"))
	// Output:
	// compiling...
	// ok
}
