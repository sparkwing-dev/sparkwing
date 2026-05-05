package storeurl

import (
	"context"
	"errors"
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

func TestS3BucketPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rest, bucket, prefix string
		wantErr              bool
	}{
		{"bucket/prefix", "bucket", "prefix", false},
		{"bucket/p1/p2", "bucket", "p1/p2", false},
		{"bucket", "bucket", "", false},
		{"bucket/", "bucket", "", false},
		{"/no-bucket", "", "", true},
	}
	for _, tc := range cases {
		b, p, err := s3BucketPrefix(tc.rest)
		if (err != nil) != tc.wantErr {
			t.Errorf("s3BucketPrefix(%q) err = %v, wantErr %v", tc.rest, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if b != tc.bucket || p != tc.prefix {
			t.Errorf("s3BucketPrefix(%q) = (%q, %q), want (%q, %q)",
				tc.rest, b, p, tc.bucket, tc.prefix)
		}
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

func TestOpenLogStore_FS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenLogStore(context.Background(), "fs://"+dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s == nil {
		t.Fatal("nil store")
	}
}

func TestOpen_BadScheme(t *testing.T) {
	t.Parallel()
	if _, err := OpenArtifactStore(context.Background(), "gcs://x"); err == nil {
		t.Error("expected err for unknown scheme")
	}
	if _, err := OpenLogStore(context.Background(), "no-scheme"); err == nil {
		t.Error("expected err for missing scheme")
	}
	_, err := OpenArtifactStore(context.Background(), "ftp://x")
	if err == nil || !strings.Contains(err.Error(), "ftp") {
		t.Errorf("err = %v, want mention of ftp", err)
	}
	_ = errors.New
}
