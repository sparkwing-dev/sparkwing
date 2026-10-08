package s3_test

import (
	"testing"

	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

func TestParseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw, bucket, prefix string
		wantErr             bool
	}{
		{"s3://bucket/prefix", "bucket", "prefix", false},
		{"s3://bucket/p1/p2", "bucket", "p1/p2", false},
		{"s3://bucket", "bucket", "", false},
		{"s3://bucket/", "bucket", "", false},
		{"s3:///no-bucket", "", "", true},
		{"fs:///tmp", "", "", true},
	}
	for _, tc := range cases {
		b, p, err := s3store.ParseURL(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseURL(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && (b != tc.bucket || p != tc.prefix) {
			t.Errorf("ParseURL(%q) = (%q, %q), want (%q, %q)", tc.raw, b, p, tc.bucket, tc.prefix)
		}
	}
}
