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

// ExampleOpenArtifactStore opens a filesystem-backed [storage.ArtifactStore]
// from a backend URL, writes a blob, and reads it back through the
// interface. Production code uses [storeurl.OpenArtifactStore] so the
// same call works against fs, s3, and sparkwing-cache backends without
// the caller knowing which is wired.
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

// ExampleLogStore_Read shows reading a per-node log through a
// [storage.LogStore] with a [storage.ReadOpts] filter. The Tail filter
// narrows the read server-side; backends that lack a native tail
// primitive emulate it. An empty ReadOpts returns the full log.
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
