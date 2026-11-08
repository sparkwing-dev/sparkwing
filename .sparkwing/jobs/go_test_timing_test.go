package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoTestJSONFlagLandsOnTheGoTestCommand(t *testing.T) {
	cases := map[string]string{
		"GOMAXPROCS=6 go test -p 6 -timeout 25m ./...":  "GOMAXPROCS=6 go test -json -p 6 -timeout 25m ./...",
		`unset X; export H=1; cd "/r" && go test ./...`: `unset X; export H=1; cd "/r" && go test -json ./...`,
		"go test ./...": "go test -json ./...",
	}
	for in, want := range cases {
		got, ok := withGoTestJSON(in)
		if !ok || got != want {
			t.Errorf("withGoTestJSON(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := withGoTestJSON("go vet ./..."); ok {
		t.Error("a script with no go test was rewritten")
	}
}

const goTestEvents = `{"Action":"start","Package":"ex/a"}
{"Action":"run","Package":"ex/a","Test":"TestSlow"}
{"Action":"output","Package":"ex/a","Test":"TestSlow","Output":"=== RUN   TestSlow\n"}
{"Action":"output","Package":"ex/a","Test":"TestSlow","Output":"--- PASS: TestSlow (2.50s)\n"}
{"Action":"pass","Package":"ex/a","Test":"TestSlow","Elapsed":2.5}
{"Action":"output","Package":"ex/a","Test":"TestBroken","Output":"=== RUN   TestBroken\n"}
{"Action":"output","Package":"ex/a","Test":"TestBroken/sub","Output":"    a_test.go:9: boom\n"}
{"Action":"output","Package":"ex/a","Test":"TestBroken/sub","Output":"    --- FAIL: TestBroken/sub (0.10s)\n"}
{"Action":"fail","Package":"ex/a","Test":"TestBroken/sub","Elapsed":0.1}
{"Action":"output","Package":"ex/a","Test":"TestBroken","Output":"--- FAIL: TestBroken (0.10s)\n"}
{"Action":"fail","Package":"ex/a","Test":"TestBroken","Elapsed":0.1}
{"Action":"output","Package":"ex/a","Output":"FAIL\n"}
{"Action":"output","Package":"ex/a","Output":"FAIL\tex/a\t2.700s\n"}
{"Action":"fail","Package":"ex/a","Elapsed":2.7}
{"Action":"output","Package":"ex/b","Output":"ok  \tex/b\t(cached)\n"}
{"Action":"pass","Package":"ex/b","Elapsed":0.4}
{"Action":"output","Package":"ex/c","Output":"?   \tex/c\t[no test files]\n"}
{"Action":"skip","Package":"ex/c","Elapsed":0}
not json at all
`

func TestGoTestTranscriptKeepsThePlainOutputAndTheTimings(t *testing.T) {
	var emitted []string
	tr := newGoTestTranscript(func(line string) { emitted = append(emitted, line) })
	for _, line := range strings.SplitAfter(goTestEvents, "\n") {
		tr.add([]byte(line))
	}
	want := []string{
		"    a_test.go:9: boom",
		"    --- FAIL: TestBroken/sub (0.10s)",
		"--- FAIL: TestBroken (0.10s)",
		"FAIL",
		"FAIL\tex/a\t2.700s",
		"ok  \tex/b\t(cached)",
		"?   \tex/c\t[no test files]",
		"not json at all",
	}
	if strings.Join(emitted, "\n") != strings.Join(want, "\n") {
		t.Fatalf("emitted:\n%s\nwant:\n%s", strings.Join(emitted, "\n"), strings.Join(want, "\n"))
	}
	if tr.plain() != strings.Join(want, "\n")+"\n" {
		t.Fatalf("plain transcript differs from what was logged: %q", tr.plain())
	}

	var timings goTestTimings
	timings.merge(tr)
	md := timings.summary(3 * time.Second)
	for _, part := range []string{"2 package(s)", "1 cached", "1 failed", "| `ex/a` (failed) | 2.7s |", "| `TestSlow` | `ex/a` | 2.5s |"} {
		if !strings.Contains(md, part) {
			t.Errorf("summary lacks %q:\n%s", part, md)
		}
	}
	if strings.Contains(md, "`ex/b` |") || strings.Contains(md, "TestBroken/sub") {
		t.Errorf("summary ranks a cached package or a subtest:\n%s", md)
	}
}

func TestFollowLinesReadsAFileStillBeingWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	got := make(chan []string, 1)
	go func() {
		var lines []string
		_ = followLines(path, done, func(b []byte) { lines = append(lines, string(b)) })
		got <- lines
	}()
	for _, chunk := range []string{"one\ntw", "o\n", "tail without newline"} {
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	close(done)
	lines := <-got
	if strings.Join(lines, "|") != "one\n|two\n|tail without newline" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestTestStepNamesTheFailingTestThroughJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "sound_test.go"),
		"package internal\n\nimport \"testing\"\n\nfunc TestSound(t *testing.T) { t.Fatal(\"named failure\") }\n")
	err := runTest(newTestContext(t))
	if err == nil {
		t.Fatal("test accepted a failing test")
	}
	for _, want := range []string{"--- FAIL: TestSound", "named failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"Action"`) {
		t.Errorf("failure carries raw JSON events:\n%v", err)
	}
}

func TestGoTestTranscriptKeepsATimedOutTestsOutput(t *testing.T) {
	tr := newGoTestTranscript(nil)
	for _, line := range []string{
		`{"Action":"output","Package":"ex/t","Test":"TestHang","Output":"=== RUN   TestHang\n"}`,
		`{"Action":"output","Package":"ex/t","Test":"TestHang","Output":"panic: test timed out after 1s\n"}`,
		`{"Action":"output","Package":"ex/t","Output":"FAIL\tex/t\t1.010s\n"}`,
		`{"Action":"fail","Package":"ex/t","Elapsed":1.01}`,
	} {
		tr.add([]byte(line))
	}
	if !strings.Contains(tr.plain(), "panic: test timed out after 1s") {
		t.Fatalf("the timeout panic was dropped:\n%s", tr.plain())
	}
}

func TestGoTestTimingsLeaveOutTestsACachedPackageReplays(t *testing.T) {
	tr := newGoTestTranscript(nil)
	for _, line := range []string{
		`{"Action":"pass","Package":"ex/c","Test":"TestOld","Elapsed":9}`,
		`{"Action":"output","Package":"ex/c","Output":"ok  \tex/c\t(cached)\n"}`,
		`{"Action":"pass","Package":"ex/c","Elapsed":9}`,
		`{"Action":"pass","Package":"ex/n","Test":"TestNew","Elapsed":1}`,
		`{"Action":"output","Package":"ex/n","Output":"ok  \tex/n\t1.0s\n"}`,
		`{"Action":"pass","Package":"ex/n","Elapsed":1}`,
	} {
		tr.add([]byte(line))
	}
	if len(tr.tests) != 1 || tr.tests[0].test != "TestNew" {
		t.Fatalf("ranked tests = %+v; want only the test that ran", tr.tests)
	}
}
