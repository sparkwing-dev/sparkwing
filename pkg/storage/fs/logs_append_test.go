package fs

import (
	"bytes"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"sync"
	"testing"
)

func TestAppendKeepsRecordsSeparateAcrossStores(t *testing.T) {
	root := t.TempDir()
	const writers, records = 8, 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	for writer := range writers {
		store, err := NewLogStore(root)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for record := range records {
				data := []byte(fmt.Sprintf("%d:%d", writer, record))
				if err := store.Append(t.Context(), "run", "node", data); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	store, err := NewLogStore(root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := store.Read(t.Context(), "run", "node", storage.ReadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(body, []byte{'\n'}), []byte{'\n'})
	if len(lines) != writers*records {
		t.Fatalf("got %d lines, want %d", len(lines), writers*records)
	}
	seen := make(map[string]bool)
	for _, line := range lines {
		seen[string(line)] = true
	}
	for writer := range writers {
		for record := range records {
			key := fmt.Sprintf("%d:%d", writer, record)
			if !seen[key] {
				t.Fatalf("record %q missing or joined to another record", key)
			}
		}
	}
}
