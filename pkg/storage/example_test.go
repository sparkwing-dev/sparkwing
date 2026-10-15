package storage_test

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func ExampleLogStore_Read() {
	dir, _ := os.MkdirTemp("", "sparkwing-example-logs-")
	defer os.RemoveAll(dir)

	ctx := context.Background()
	logs, err := fs.NewLogStore(dir)
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	for _, line := range []string{"compiling app...\n", "running tests...\n", "all green\n"} {
		if err := logs.Append(ctx, "run-42", "build", []byte(line)); err != nil {
			fmt.Println("append:", err)
			return
		}
	}

	body, err := logs.Read(ctx, "run-42", "build", storage.ReadOpts{Tail: 1})
	if err != nil {
		fmt.Println("read:", err)
		return
	}
	fmt.Print(string(bytes.TrimRight(body, "\n")))
	// Output: all green
}
