package bincache

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
)

func TestCacheQueueLockReusesItsRetryTimer(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "entry_queue.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "openCacheQueueLock" {
			target = fn
		}
	}
	if target == nil {
		t.Fatal("openCacheQueueLock declaration is missing")
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, token.NewFileSet(), target); err != nil {
		t.Fatal(err)
	}
	want := `func openCacheQueueLock(ctx context.Context, root string) (*os.File, error) {
	waitCtx, cancel := context.WithTimeout(ctx, cacheQueueLockTimeout)
	defer cancel()
	retry := time.NewTimer(cacheQueueLockRetry)
	defer retry.Stop()
	for {
		lock, acquired, err := openCacheLock(root, "entry-queue", cacheLockExclusiveNonblock)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		retry.Reset(cacheQueueLockRetry)
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, ErrCacheQueueBusy
		case <-retry.C:
		}
	}
}`
	if formatted.String() != want {
		t.Fatalf("openCacheQueueLock must reuse one owned retry timer; got:\n%s", formatted.String())
	}
}
