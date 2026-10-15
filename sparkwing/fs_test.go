package sparkwing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathJoinsFromWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := Path("src", "main.go"); got != filepath.Join(cwd, "src", "main.go") {
		t.Fatalf("Path = %q", got)
	}
}

func TestReadWriteFileAndGlob(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "output.txt")
	if err := WriteFile(file, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(file)
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	matches, err := Glob(filepath.Join(root, "*.txt"))
	if err != nil || len(matches) != 1 || matches[0] != file {
		t.Fatalf("Glob = %v, %v", matches, err)
	}
}
