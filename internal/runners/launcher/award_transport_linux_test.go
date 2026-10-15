package launcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadBootstrapAwardUsesProjectedSecretLayout(t *testing.T) {
	award, resources := transportAward(t)
	directory := t.TempDir()
	projection := filepath.Join(directory, "..projection")
	if err := os.Mkdir(projection, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..projection", filepath.Join(directory, "..data")); err != nil {
		t.Fatal(err)
	}
	for name, body := range resources.BootstrapSecret.Data {
		if err := os.WriteFile(filepath.Join(projection, name), body, 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadBootstrapAward(filepath.Join(directory, "bootstrap"))
	if err != nil || !reflect.DeepEqual(got, award) {
		t.Fatalf("projected Secret lost the private award: %v", err)
	}
}
