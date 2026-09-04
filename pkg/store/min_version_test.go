package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMinVersion_FreshOpenStampsBinaryVersion(t *testing.T) {
	store.SetBinaryVersion("v0.16.0")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	target := storetest.New(t)
	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var got string
	if err := st.DB().QueryRow(
		`SELECT value FROM sparkwing_meta WHERE key = 'min_binary_version'`).Scan(&got); err != nil {
		t.Fatalf("read min_binary_version: %v", err)
	}
	if got != "v0.16.0" {
		t.Errorf("min_binary_version = %q, want v0.16.0", got)
	}
}

func TestSkew_MessageNamesRequirementsAndVersions(t *testing.T) {
	target := storetest.New(t)

	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version) VALUES
		 ('webhook-replay-keys', 1, 'v0.41.0'), ('unique-token-prefixes', 1, 'v0.40.0')`); err != nil {
		t.Fatalf("seed future requirements: %v", err)
	}
	_ = st.Close()

	store.SetBinaryVersion("v0.38.2")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	_, err = target.TryOpen()
	if err == nil {
		t.Fatal("Open against a DB listing unknown requirements should fail")
	}
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.MinVersion != "v0.41.0" {
		t.Errorf("skew.MinVersion = %q, want v0.41.0 (the highest stamp among the unknown)", skew.MinVersion)
	}
	if skew.InstalledVersion != "v0.38.2" {
		t.Errorf("skew.InstalledVersion = %q, want v0.38.2", skew.InstalledVersion)
	}
	want := "sparkwing: this state database uses unique-token-prefixes and webhook-replay-keys, " +
		"which need sparkwing >= v0.41.0; you have v0.38.2. Run `sparkwing update` to upgrade."
	if got := err.Error(); got != want {
		t.Errorf("skew message =\n%s\nwant\n%s", got, want)
	}
}

func TestSkew_DevelStampNamesANewerBuild(t *testing.T) {
	target := storetest.New(t)

	st, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version)
		 VALUES ('webhook-replay-keys', 1, '(devel)')`); err != nil {
		t.Fatalf("seed future requirement: %v", err)
	}
	_ = st.Close()

	store.SetBinaryVersion("v0.38.2")
	t.Cleanup(func() { store.SetBinaryVersion("") })

	_, err = target.TryOpen()
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.MinVersion != "" {
		t.Errorf("skew.MinVersion = %q, want empty for a development stamp", skew.MinVersion)
	}
	want := "sparkwing: this state database uses webhook-replay-keys, which needs a newer build " +
		"than v0.38.2. Run `sparkwing update` to upgrade."
	if got := err.Error(); got != want {
		t.Errorf("skew message =\n%s\nwant\n%s", got, want)
	}
}

func TestCurrentSchemaVersion_ReportsRecorded(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	got, err := st.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if got != store.ExpectedSchemaVersion() {
		t.Errorf("CurrentSchemaVersion = %d, want %d", got, store.ExpectedSchemaVersion())
	}
}
