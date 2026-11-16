package depcache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"testing"
)

func TestExtractArchiveChecksGzipTail(t *testing.T) {
	for _, test := range []struct {
		name            string
		padding         int
		corrupt, refuse bool
	}{
		{name: "ordinary padding", padding: 10 << 10},
		{name: "padding limit", padding: 1 << 20},
		{name: "tail bomb", padding: 2 << 20, refuse: true},
		{name: "checksum", corrupt: true, refuse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := gz.Write(make([]byte, test.padding)); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			if test.corrupt {
				data[len(data)-8] ^= 1
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = extractArchiveRoot(bytes.NewReader(data), root)
			if (err != nil) != test.refuse {
				t.Fatalf("extractArchiveRoot() = %v, refusal = %v", err, test.refuse)
			}
		})
	}
}
