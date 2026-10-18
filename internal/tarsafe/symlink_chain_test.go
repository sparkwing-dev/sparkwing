package tarsafe

import (
	"archive/tar"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractRefusesASymlinkChainOutOfTheRoot(t *testing.T) {
	for name, entries := range map[string][]*tar.Header{
		"through an earlier link": {
			{Name: "d", Typeflag: tar.TypeSymlink, Linkname: "."},
			{Name: "e", Typeflag: tar.TypeSymlink, Linkname: "d/../outside-secret"},
		},
		"through a later link": {
			{Name: "e", Typeflag: tar.TypeSymlink, Linkname: "x/y/outside-secret"},
			{Name: "x", Typeflag: tar.TypeSymlink, Linkname: "."},
			{Name: "y", Typeflag: tar.TypeSymlink, Linkname: ".."},
		},
	} {
		parent := t.TempDir()
		if err := os.WriteFile(filepath.Join(parent, "outside-secret"), []byte("s"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(parent, "root")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		err := ExtractInRoot(rawTar(t, entries, nil), dir, Policy{AllowSymlinks: true})
		if err == nil {
			got, _ := filepath.EvalSymlinks(filepath.Join(dir, "e"))
			t.Fatalf("%s: extraction accepted e, which resolves to %s", name, got)
		}
	}
}

// Invariant: after an extraction succeeds, no extracted path resolves outside the root.
func TestExtractedSymlinksStayInsideTheRootProperty(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	names := []string{"a", "b", "c", "a/x", "b/y"}
	parts := []string{".", "..", "a", "b", "c", "x", "y", "outside-secret"}
	for iter := range 3000 {
		parent := t.TempDir()
		if err := os.WriteFile(filepath.Join(parent, "outside-secret"), []byte("s"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(parent, "root")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var entries []*tar.Header
		for range 1 + r.Intn(5) {
			var link []string
			for range 1 + r.Intn(4) {
				link = append(link, parts[r.Intn(len(parts))])
			}
			entries = append(entries, &tar.Header{Name: names[r.Intn(len(names))], Typeflag: tar.TypeSymlink, Linkname: strings.Join(link, "/")})
		}
		if ExtractInRoot(rawTar(t, entries, nil), dir, Policy{AllowSymlinks: true}) != nil {
			continue
		}
		_ = filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if got, err := filepath.EvalSymlinks(path); err == nil && got != dir && !strings.HasPrefix(got, dir+string(filepath.Separator)) {
				t.Fatalf("iter %d: %s resolves to %s outside the root; entries %v", iter, path, got, describe(entries))
			}
			return nil
		})
	}
}

func describe(entries []*tar.Header) []string {
	var out []string
	for _, h := range entries {
		out = append(out, h.Name+"->"+h.Linkname)
	}
	return out
}
