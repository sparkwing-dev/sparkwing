package storeurl

import (
	"context"
	"strings"
	"testing"
)

func TestSplitScheme(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in               string
		wantScheme, rest string
		wantErr          bool
	}{
		{"fs:///tmp/a", "fs", "/tmp/a", false},
		{"s3://bucket/prefix", "s3", "bucket/prefix", false},
		{"s3://bucket", "s3", "bucket", false},
		{"no-scheme", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		s, r, err := splitScheme(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("splitScheme(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if s != tc.wantScheme || r != tc.rest {
			t.Errorf("splitScheme(%q) = (%q, %q), want (%q, %q)",
				tc.in, s, r, tc.wantScheme, tc.rest)
		}
	}
}

func TestFsPath(t *testing.T) {
	t.Parallel()
	if _, err := fsPath("/abs/path"); err != nil {
		t.Errorf("absolute: %v", err)
	}
	if _, err := fsPath("relative"); err == nil {
		t.Errorf("relative: expected err")
	}
	if _, err := fsPath(""); err == nil {
		t.Errorf("empty: expected err")
	}
	got, err := fsPath("~/sparkwing")
	if err != nil {
		t.Errorf("home: %v", err)
	}
	if strings.HasPrefix(got, "~") {
		t.Errorf("home not expanded: %q", got)
	}
}

func TestOpenArtifactStore_FS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenArtifactStore(context.Background(), "fs://"+dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s == nil {
		t.Fatal("nil store")
	}
}

func TestOpen_BadScheme(t *testing.T) {
	t.Parallel()
	if _, err := OpenArtifactStore(context.Background(), "no-scheme"); err == nil {
		t.Error("expected err for missing scheme")
	}
	for _, scheme := range []string{"ftp", "http", "https"} {
		_, err := OpenArtifactStore(t.Context(), scheme+"://objects.invalid")
		if err == nil || !strings.Contains(err.Error(), scheme) {
			t.Errorf("%s: err = %v, want an unsupported scheme", scheme, err)
		}
	}
}
