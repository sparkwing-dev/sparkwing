package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseTargetBuildsAllSupportedBinaries(t *testing.T) {
	script, err := filepath.Abs("build-release-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(target+"/"+arch, func(t *testing.T) {
				dir := t.TempDir()
				goTool := "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n if [ \"$1\" = -o ]; then shift; printf '%s' \"$GOOS/$GOARCH\" >\"$1\"; exit 0; fi\n shift\ndone\nexit 1\n"
				if err := os.WriteFile(filepath.Join(dir, "go"), []byte(goTool), 0o700); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("bash", script)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GOOS="+target, "GOARCH="+arch, "TAG=v0.52.4")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				files, err := os.ReadDir(filepath.Join(dir, "dist"))
				if err != nil {
					t.Fatal(err)
				}
				want := 6
				if target == "windows" {
					want = 2
				}
				if len(files) != want {
					t.Fatalf("outputs=%d want %d", len(files), want)
				}
				for _, file := range files {
					name := file.Name()
					if !strings.Contains(name, "-"+target+"-"+arch) {
						t.Fatal(name)
					}
					if target == "windows" && name != "sparkwing-windows-"+arch+".exe" && name != "sparkwing-runner-windows-"+arch+".exe" {
						t.Fatal(name)
					}
				}
			})
		}
	}
}
