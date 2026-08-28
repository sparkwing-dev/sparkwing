package storage_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func ExampleOpenArtifactStore() {
	dir, err := os.MkdirTemp("", "sparkwing-example-")
	if err != nil {
		fmt.Println("mkdir:", err)
		return
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()
	store, err := storeurl.OpenArtifactStore(ctx, "fs://"+dir)
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	if err := store.Put(ctx, "build/abc123/manifest.json", strings.NewReader(`{"image":"app:dev"}`)); err != nil {
		fmt.Println("put:", err)
		return
	}

	r, err := store.Get(ctx, "build/abc123/manifest.json")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	defer r.Close()

	body, _ := io.ReadAll(r)
	fmt.Println(string(body))
	// Output: {"image":"app:dev"}
}

func ExampleLogStore_Read() {
	dir, _ := os.MkdirTemp("", "sparkwing-example-logs-")
	defer os.RemoveAll(dir)

	ctx := context.Background()
	logs, err := storeurl.OpenLogStore(ctx, "fs://"+dir)
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
