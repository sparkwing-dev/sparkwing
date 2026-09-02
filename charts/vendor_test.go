package charts

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	bundleSourceDir = "sparkwing-runner-bundle"
	vendorDir       = "sparkwing-full/charts"
)

func chartVersion(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	if chart.Version == "" {
		t.Fatalf("%s declares no version", path)
	}
	return chart.Version
}

func lockedBundleVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("sparkwing-full/Chart.lock")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Dependencies []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	for _, dep := range lock.Dependencies {
		if dep.Name == bundleSourceDir {
			return dep.Version
		}
	}
	t.Fatalf("Chart.lock does not pin %s", bundleSourceDir)
	return ""
}

func vendoredTarball(t *testing.T) string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(vendorDir, "*.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("vendored sub-charts = %v, want exactly one; delete the stale tarballs and run "+
			"helm dependency update ./charts/sparkwing-full", found)
	}
	return found[0]
}

func tarballFiles(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer gz.Close()
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), bundleSourceDir+"/")
		if name != "values.yaml" && !strings.HasPrefix(name, "templates/") {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s from %s: %v", hdr.Name, path, err)
		}
		out[name] = string(body)
	}
	return out
}

func sourceChartFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(bundleSourceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(path), bundleSourceDir+"/")
		if name != "values.yaml" && !strings.HasPrefix(name, "templates/") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[name] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestVendoredRunnerBundleMatchesItsSource(t *testing.T) {
	version := chartVersion(t, filepath.Join(bundleSourceDir, "Chart.yaml"))
	tarball := vendoredTarball(t)
	const refresh = "run helm dependency update ./charts/sparkwing-full and commit the tarball and Chart.lock"

	if want := bundleSourceDir + "-" + version + ".tgz"; filepath.Base(tarball) != want {
		t.Fatalf("vendored sub-chart = %s, want %s; %s", filepath.Base(tarball), want, refresh)
	}
	if got := lockedBundleVersion(t); got != version {
		t.Errorf("Chart.lock pins %s, source chart is %s; %s", got, version, refresh)
	}

	vendored := tarballFiles(t, tarball)
	source := sourceChartFiles(t)
	if got, want := sortedKeys(vendored), sortedKeys(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("vendored files = %v, source files = %v; %s", got, want, refresh)
	}
	for _, name := range sortedKeys(source) {
		if vendored[name] != source[name] {
			t.Errorf("vendored %s differs from %s/%s; %s", name, bundleSourceDir, name, refresh)
		}
	}
}
