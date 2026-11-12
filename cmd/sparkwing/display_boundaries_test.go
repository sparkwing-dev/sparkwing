package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func TestHumanOutputComputeCount(t *testing.T) {
	v := controllerwire.OperatorLimits{Usage: controllerwire.OperatorUsage{ByPrincipal: map[string]int64{"runner\x1b[2J": 7}}}
	for name, fn := range map[string]func(*bytes.Buffer) error{"pretty": func(b *bytes.Buffer) error { return renderComputeLimits(b, v) }, "plain": func(b *bytes.Buffer) error { return writeComputeLimitsPlain(b, v) }} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if e := fn(&b); e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(b.String(), "7\n") {
				t.Fatalf("lost runner count: %q", b.String())
			}
		})
	}
}

func TestHumanOutputScheduleInstallation(t *testing.T) {
	bad := "repo\x1b[2J/ci/default"
	v := scheduleInstallation{ScheduleRepoWritten: controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{{ID: "id", Name: bad}}, Withdrawn: []string{bad}}, Skipped: []string{bad}}
	var b bytes.Buffer
	if e := renderScheduleInstallation(&b, v, "pretty"); e != nil {
		t.Fatal(e)
	}
	if strings.ContainsRune(b.String(), '\x1b') {
		t.Fatalf("active control: %q", b.String())
	}
}

func TestHumanOutputSparkLint(t *testing.T) {
	dir := writeSparkFixture(t, map[string]string{"spark.json": `{"name":"demo","description":"test","author":"tester","packages":[{"path":"helpers","description":"ok"}],"dependencies":[{"source":"evil\u001b[2J"}]}`, "helpers/doc.go": "package helpers\n"})
	var e error
	out := captureStderr(t, func() { e = runSparksLintArgs([]string{"--path", dir, "-o", "pretty"}) })
	if e == nil {
		t.Fatal("expected missing version")
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("active control: %q", out)
	}
}

func TestHumanOutputRepoInfoColor(t *testing.T) {
	old := color.Enabled()
	color.SetEnabled(true)
	defer color.SetEnabled(old)
	out := captureStdout(t, func() { printRepoInfo(repoInfo{Name: "repo", Primary: "/tmp/repo", Dirty: true}) })
	if strings.Contains(out, `\x1b[33m`) {
		t.Fatalf("trusted color escaped: %q", out)
	}
}

func TestHumanOutputDebugRawAndPlainTail(t *testing.T) {
	input := "ok\x1b[2J\rforged"
	var b bytes.Buffer
	_, e := (debugOutputWriter{out: &b, format: "pretty"}).Write([]byte(input))
	if e != nil {
		t.Fatal(e)
	}
	if b.String() != input {
		t.Fatalf("debug stream changed %q", b.String())
	}
	b.Reset()
	e = writeLogRecord(&b, controllerwire.LogRecord{Line: input}, "plain", false)
	if e != nil {
		t.Fatal(e)
	}
	if b.String() != input+"\n" {
		t.Fatalf("plain stream changed %q", b.String())
	}
	t.Logf("raw debug %q and raw plain tail %q", input, b.String())
}

func TestHumanOutputVersionPolicyReason(t *testing.T) {
	var b strings.Builder
	writeVersionPolicyLine(&b, &versionPolicyStatus{Status: "unavailable", Reason: "open cache\x1b[2J/policy: denied"})
	if strings.ContainsRune(b.String(), '\x1b') {
		t.Fatalf("active control: %q", b.String())
	}
}

func TestHumanOutputRunnerRevocationHint(t *testing.T) {
	out := captureStderr(t, func() { printRunnerRevokeHint("profile\x1b[2J", "sw_fixture") })
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("active control in hint: %q", out)
	}
}

func TestHumanOutputSparkEmptyListAndLintPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot contain ESC")
	}
	root := writeSparkFixture(t, map[string]string{
		"repo\x1b[2J/.sparkwing/go.mod":      "module example.com/fixture\n",
		"repo\x1b[2J/library/spark.json":     `{"name":"demo","description":"test","author":"tester","packages":[{"path":"helpers","description":"ok"}]}`,
		"repo\x1b[2J/library/helpers/doc.go": "package helpers\n",
	})
	t.Chdir(filepath.Join(root, "repo\x1b[2J"))
	inv, err := clisurface.Parse("sparkwing", []string{"pipeline", "sparks", "list", "--no-resolve", "-o", "pretty"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { err = runSparksList(inv) })
	if err != nil || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("empty list: %q (%v)", out, err)
	}
	for _, format := range []string{"plain", "pretty"} {
		out = captureStdout(t, func() {
			err = runSparksLintArgs([]string{"--path", filepath.Join(root, "repo\x1b[2J/library"), "-o", format})
		})
		if err != nil || strings.ContainsRune(out, '\x1b') {
			t.Fatalf("lint %s: %q (%v)", format, out, err)
		}
	}
}

func TestHumanOutputSparkInflateReceiptPreservesJSON(t *testing.T) {
	bad := "/repo\x1b[2J/.sparkwing"
	for _, format := range []string{"pretty", "plain", "json"} {
		var out bytes.Buffer
		err := runSparksInflateCommandArgs(t.Context(), []string{"--library", "templates", "-o", format}, &out,
			func() (string, error) { return bad, nil },
			func(context.Context, string, string) (*sparks.VendorResult, error) {
				return &sparks.VendorResult{ModulePath: "example.com/lib", Version: "v1.0.0", Dest: bad + "/sparks/lib", RelReplace: "../repo\x1b[2J/sparks/lib"}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var got map[string]string
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["dest"] != bad+"/sparks/lib" {
				t.Fatalf("JSON changed: %q (%v)", out.String(), err)
			}
		} else if strings.ContainsRune(out.String(), '\x1b') {
			t.Fatalf("%s receipt: %q", format, out.String())
		}
	}
}

func TestHumanOutputPlainMetadata(t *testing.T) {
	bad := "name\x1b[2J\r\u202e"
	for name, render := range map[string]func(*bytes.Buffer) error{
		"stats": func(out *bytes.Buffer) error {
			return writeRunStatistics(out, []pipelineStats{{Pipeline: bad}}, "plain")
		},
		"sizing reset": func(out *bytes.Buffer) error {
			return writeSizingReset(out, controllerwire.SizingResetSummary{Pipelines: []string{bad}}, "plain")
		},
		"sizing list": func(out *bytes.Buffer) error {
			inv, err := clisurface.Parse("sparkwing", []string{"runs", "stats", "--sizing", "-o", "plain"})
			if err != nil {
				return err
			}
			manager := &sizingManager{list: func(controllerwire.SizingQuery) (controllerwire.SizingPage, error) {
				return controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{{Repo: "acme/repo", Pipeline: bad, Source: "measured"}}}, nil
			}}
			return observeSizing(t.Context(), inv, manager, out)
		},
		"schedule": func(out *bytes.Buffer) error {
			return renderScheduleRecord(out, controllerwire.ScheduleUpcoming{Name: bad, At: "later"}, bad+"\tlater", "plain")
		},
		"secret rotation": func(out *bytes.Buffer) error {
			return renderSecretRotation(out, controllerwire.SecretRotation{Skipped: []controllerwire.SecretSelector{{Name: "TOKEN", Repo: bad, Pipeline: bad}}}, "plain")
		},
		"log search": func(out *bytes.Buffer) error {
			return writeLogRecord(out, controllerwire.LogRecord{RunID: "run", JobID: bad, Line: bad}, "plain", true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := render(&out); err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(out.String(), "\x1b\r\u202e") {
				t.Fatalf("active controls: %q", out.String())
			}
			if name == "schedule" && strings.Count(out.String(), "\t") != 1 {
				t.Fatalf("layout delimiter changed: %q", out.String())
			}
		})
	}
	inv, err := clisurface.Parse("sparkwing", []string{"repos", "add", "--checkout", "/fixture", "-o", "plain"})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() { err = writeRepositoryMutation(inv, "added", bad, 1) })
	if err != nil || strings.ContainsAny(out, "\x1b\r\u202e") {
		t.Fatalf("repository receipt: %q (%v)", out, err)
	}
}
