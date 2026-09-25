package userconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fleetish struct {
	Listen string   `yaml:"listen"`
	Names  []string `yaml:"names,omitempty"`
	Max    int      `yaml:"max"`
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Filename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PathEnv, path)
	return path
}

func TestReadDecodesOneSectionAndKeepsSeededDefaults(t *testing.T) {
	path := writeFile(t, "profiles:\n  laptop: {}\nfleet:\n  listen: 127.0.0.1:7443\n")
	got := fleetish{Max: 3}
	found, err := Read(path, Fleet, &got)
	if err != nil || !found {
		t.Fatalf("Read = %v, %v", found, err)
	}
	if got.Listen != "127.0.0.1:7443" || got.Max != 3 {
		t.Fatalf("section = %+v, want the listen address and the seeded max", got)
	}
}

func TestReadReportsAnAbsentFileOrSection(t *testing.T) {
	var got fleetish
	if found, err := Read(filepath.Join(t.TempDir(), Filename), Fleet, &got); err != nil || found {
		t.Fatalf("absent file: Read = %v, %v", found, err)
	}
	path := writeFile(t, "# nothing yet\n")
	if found, err := Read(path, Fleet, &got); err != nil || found {
		t.Fatalf("empty file: Read = %v, %v", found, err)
	}
	path = writeFile(t, "profiles: {}\nfleet:\n")
	if found, err := Read(path, Fleet, &got); err != nil || found {
		t.Fatalf("null section: Read = %v, %v", found, err)
	}
}

func TestReadRejectsWhatItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"unknown key in the section", "fleet:\n  listen: x\n  token: nope\n", "line 3: field token not found"},
		{"unknown section", "fleet:\n  listen: x\nbudget: 6\n", `unknown section "budget"`},
		{"repeated section", "fleet: {}\nfleet: {}\n", "appears twice"},
		{"second document", "fleet: {}\n---\nfleet: {}\n", "multiple YAML documents"},
		{"not a mapping", "- fleet\n", "must be a mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got fleetish
			_, err := Read(writeFile(t, tc.body), Fleet, &got)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Read error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadRefusesAFileOthersCanRead(t *testing.T) {
	if os.Getuid() < 0 {
		t.Skip("owner-only modes are a unix check")
	}
	path := writeFile(t, "fleet: {}\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var got fleetish
	if _, err := Read(path, Fleet, &got); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("Read error = %v, want an owner-only refusal", err)
	}
}

func TestUpdateKeepsOtherSectionsAndComments(t *testing.T) {
	path := writeFile(t, `# machine settings
profiles:
  # the laptop connection
  laptop:
    controller:
      url: http://127.0.0.1:4344 # local
admission:
  budget: 50%,8gb
fleet:
  listen: 127.0.0.1:1
`)
	var got fleetish
	err := Update(path, Fleet, "the fleet config", &got, func(found bool) error {
		if !found || got.Listen != "127.0.0.1:1" {
			return fmt.Errorf("current section = %v %+v", found, got)
		}
		got.Listen = "127.0.0.1:2"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# machine settings", "# the laptop connection", "# local", "budget: 50%,8gb", "listen: 127.0.0.1:2"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rewritten file lost %q:\n%s", want, body)
		}
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 && os.Getuid() >= 0 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestUpdateLeavesTheFileAloneWhenTheChangeFails(t *testing.T) {
	path := writeFile(t, "fleet:\n  listen: a\n")
	before, _ := os.ReadFile(path)
	var got fleetish
	refused := errors.New("already there")
	if err := Update(path, Fleet, "the fleet config", &got, func(bool) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("Update error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("file changed:\n%s", after)
	}
}

func TestWriteCreatesTheFileAndDropsAnEmptySection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", Filename)
	t.Setenv(PathEnv, path)
	if err := Write(path, Repos, "the repo registry", map[string]any{"fallback_paths": []string{"~/code"}}); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, Fleet, "the fleet config", fleetish{Listen: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, Repos, "the repo registry", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "repos") || !strings.Contains(string(body), "listen: x") {
		t.Fatalf("file = %q, want only the fleet section", body)
	}
}

func TestConcurrentWritersOfDifferentSectionsKeepBoth(t *testing.T) {
	path := writeFile(t, "")
	var wg sync.WaitGroup
	const writers = 6
	errs := make(chan error, 2*writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			section := []string{Fleet, Agent, Admission}[i%3]
			own := map[string]int{}
			errs <- Update(path, section, section, &own, func(bool) error { own[fmt.Sprint(i)] = i; return nil })
			var n []int
			errs <- Update(path, Repos, "repos", &n, func(bool) error { n = append(n, i); return nil })
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []int
	if _, err := Read(path, Repos, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != writers {
		t.Fatalf("repos = %v, want one entry from each of %d writers", got, writers)
	}
	for _, section := range []string{Fleet, Agent, Admission} {
		own := map[string]int{}
		if _, err := Read(path, section, &own); err != nil || len(own) != 2 {
			t.Fatalf("%s = %v, %v; want the entries of both of its writers", section, own, err)
		}
	}
}

func TestPathHonorsTheOverrideAndStaysInTheSandboxUnderTest(t *testing.T) {
	want := filepath.Join(t.TempDir(), "elsewhere.yaml")
	t.Setenv(PathEnv, want)
	if got, err := Path(); err != nil || got != want {
		t.Fatalf("Path = %q, %v; want %q", got, err, want)
	}
	t.Setenv(PathEnv, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, os.TempDir()) || filepath.Base(got) != Filename {
		t.Fatalf("Path = %q, want config.yaml under %s", got, os.TempDir())
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, err := Path(); err != nil || got != filepath.Join(xdg, "sparkwing", Filename) {
		t.Fatalf("Path = %q, %v", got, err)
	}
}

func TestWriteRefusesToDropAnAnchorAnAliasElsewhereUses(t *testing.T) {
	body := "fleet: &shared\n  listen: a\nagent: *shared\n"
	path := writeFile(t, body)
	err := Write(path, Fleet, "the fleet config", map[string]string{"listen": "b"})
	if err != nil {
		t.Fatalf("an update that keeps the anchored node failed: %v", err)
	}
	var agent map[string]string
	if _, err := Read(path, Agent, &agent); err != nil || agent["listen"] != "b" {
		t.Fatalf("agent = %v, %v; want the alias still resolving", agent, err)
	}

	path = writeFile(t, "fleet:\n  inner: &shared\n    listen: a\nagent: *shared\n")
	before := readBack(t, path)
	err = Write(path, Fleet, "the fleet config", map[string]string{"listen": "b"})
	if err == nil || !strings.Contains(err.Error(), "&shared") {
		t.Fatalf("Write error = %v, want a refusal naming the anchor", err)
	}
	if after := readBack(t, path); after != before {
		t.Fatalf("a refused write changed the file:\n%s", after)
	}
}

func TestWriteKeepsCommentsInsideTheRewrittenSection(t *testing.T) {
	path := writeFile(t, "profiles:\n  # production\n  prod:\n    controller:\n      url: http://a # the old one\n  # lab\n  lab: {}\n")
	if err := Write(path, Profiles, "profiles", map[string]any{
		"prod": map[string]any{"controller": map[string]any{"url": "http://b"}},
		"lab":  map[string]any{},
		"new":  map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	body := readBack(t, path)
	for _, want := range []string{"# production", "url: http://b # the old one", "# lab", "new: {}"} {
		if !strings.Contains(body, want) {
			t.Errorf("rewritten section lacks %q:\n%s", want, body)
		}
	}
}

func TestWriteToACommentOnlyFileKeepsTheComment(t *testing.T) {
	path := writeFile(t, "# this machine's settings\n")
	if err := Write(path, Fleet, "the fleet config", fleetish{Listen: "x"}); err != nil {
		t.Fatal(err)
	}
	if body := readBack(t, path); !strings.Contains(body, "# this machine's settings") || !strings.Contains(body, "listen: x") {
		t.Fatalf("file = %q", body)
	}
}

func readBack(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
