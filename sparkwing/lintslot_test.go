package sparkwing_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func lintSlotTool(t *testing.T) string {
	t.Helper()
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, t.Name())
	tool := "lintslottest-" + safe
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(os.TempDir(), "sparkwing-toolcache", "lintslots", tool))
	})
	return tool
}

func acquireFor(t *testing.T, tool, dir string) *sparkwing.LintSlot {
	t.Helper()
	sparkwing.SetWorkDir(dir)
	slot, err := sparkwing.AcquireLintSlot(tool)
	if err != nil {
		t.Fatalf("acquire slot for %s: %v", dir, err)
	}
	t.Cleanup(slot.Release)
	return slot
}

func resolves(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return real
}

func TestAcquireLintSlot_CanonicalPathLeadsToTheWorktree(t *testing.T) {
	wt := t.TempDir()
	useWorkDir(t, wt)

	slot := acquireFor(t, lintSlotTool(t), wt)
	if !slot.Canonical {
		t.Fatalf("first lease was not canonical: %+v", slot)
	}
	if got, want := resolves(t, slot.Path), resolves(t, wt); got != want {
		t.Fatalf("slot path leads to %q, want the worktree %q", got, want)
	}
	if slot.Path == wt {
		t.Fatalf("slot path %q is the worktree itself, so nothing is shared", slot.Path)
	}
}

func TestAcquireLintSlot_ReuseKeepsTheCacheAndFollowsTheNewHolder(t *testing.T) {
	tool := lintSlotTool(t)
	donor, target := t.TempDir(), t.TempDir()
	useWorkDir(t, donor)

	first := acquireFor(t, tool, donor)
	firstPath, firstCache := first.Path, first.Cache
	first.Release()

	second := acquireFor(t, tool, target)
	if second.Cache != firstCache {
		t.Fatalf("second holder got cache %q, want the released slot's %q -- a new "+
			"worktree would pay a cold start", second.Cache, firstCache)
	}
	if second.Path != firstPath {
		t.Fatalf("second holder got path %q, want %q -- a cache filled at the first "+
			"path would replay it into the second", second.Path, firstPath)
	}
	if got, want := resolves(t, second.Path), resolves(t, target); got != want {
		t.Fatalf("reused slot still leads to %q, want the new holder %q", got, want)
	}
}

func TestAcquireLintSlot_ConcurrentHoldersGetDifferentSlots(t *testing.T) {
	tool := lintSlotTool(t)
	first, second := t.TempDir(), t.TempDir()

	a := acquireFor(t, tool, first)
	b := acquireFor(t, tool, second)

	if !a.Canonical || !b.Canonical {
		t.Fatalf("expected two canonical slots, got %+v and %+v", a, b)
	}
	if a.Path == b.Path {
		t.Fatalf("both holders got slot path %q", a.Path)
	}
	if a.Cache == b.Cache {
		t.Fatalf("both holders got cache %q", a.Cache)
	}
	if got, want := resolves(t, a.Path), resolves(t, first); got != want {
		t.Fatalf("slot A leads to %q, want %q", got, want)
	}
	if got, want := resolves(t, b.Path), resolves(t, second); got != want {
		t.Fatalf("slot B leads to %q, want %q", got, want)
	}
}

func TestAcquireLintSlot_FallsBackToThePrivateCacheWhenAllSlotsAreBusy(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, "1")
	tool := lintSlotTool(t)
	held, waiting := t.TempDir(), t.TempDir()

	acquireFor(t, tool, held)

	useWorkDir(t, waiting)
	fallback := acquireFor(t, tool, waiting)
	if fallback.Canonical {
		t.Fatalf("second lease claimed a canonical slot while the only slot was held: %+v", fallback)
	}
	if fallback.Path != waiting {
		t.Fatalf("fallback path is %q, want the worktree %q", fallback.Path, waiting)
	}
	if want := sparkwing.ToolCacheDir(tool); fallback.Cache != want {
		t.Fatalf("fallback cache is %q, want the private %q", fallback.Cache, want)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sparkwing.ToolCacheDir(tool)) })
}

func TestLintSlot_ReleaseFreesTheSlotAndRepeatsSafely(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, "1")
	tool := lintSlotTool(t)
	first, second := t.TempDir(), t.TempDir()

	slot := acquireFor(t, tool, first)
	slot.Release()
	slot.Release()

	again := acquireFor(t, tool, second)
	if !again.Canonical {
		t.Fatalf("slot was not reclaimable after Release: %+v", again)
	}
}

func TestAcquireLintSlot_SlotCountHonoursTheEnvOverride(t *testing.T) {
	t.Setenv(sparkwing.LintSlotsEnv, "2")
	tool := lintSlotTool(t)

	a := acquireFor(t, tool, t.TempDir())
	b := acquireFor(t, tool, t.TempDir())
	c := acquireFor(t, tool, t.TempDir())

	if !a.Canonical || !b.Canonical {
		t.Fatalf("two slots were configured but only got %+v, %+v", a, b)
	}
	if c.Canonical {
		t.Fatalf("third lease claimed a slot with only two configured: %+v", c)
	}
	t.Cleanup(func() { _ = os.RemoveAll(c.Cache) })
}

func TestLintSlotConfigure_SetsPWDAndTheCacheVariable(t *testing.T) {
	wt := t.TempDir()
	useWorkDir(t, wt)
	slot := acquireFor(t, lintSlotTool(t), wt)

	cmd := slot.Configure(sparkwing.Bash(context.Background(), `echo "$PWD"; echo "$TOOL_CACHE"`), "TOOL_CACHE")
	out, err := cmd.Lines()
	if err != nil {
		t.Fatalf("read the slot environment: %v", err)
	}
	if len(out) < 2 {
		t.Fatalf("got %v, want PWD and TOOL_CACHE", out)
	}
	if out[0] != slot.Path {
		t.Fatalf("PWD is %q, want the slot path %q -- the linter would resolve the "+
			"symlink and cache the worktree's own path", out[0], slot.Path)
	}
	if out[1] != slot.Cache {
		t.Fatalf("TOOL_CACHE is %q, want %q", out[1], slot.Cache)
	}
}

func TestLintSlotConfigureIn_KeepsTheSubmoduleOnTheCanonicalPath(t *testing.T) {
	wt := t.TempDir()
	sub := filepath.Join(wt, "tools")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("seed submodule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "who.txt"), []byte("submodule"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	useWorkDir(t, wt)
	slot := acquireFor(t, lintSlotTool(t), wt)

	cmd := slot.ConfigureIn(sparkwing.Bash(context.Background(), `echo "$PWD"; cat who.txt`), "tools", "TOOL_CACHE")
	out, err := cmd.Lines()
	if err != nil {
		t.Fatalf("run in submodule: %v", err)
	}
	want := filepath.Join(slot.Path, "tools")
	if out[0] != want {
		t.Fatalf("PWD is %q, want the canonical submodule path %q -- the linter would "+
			"resolve the symlink and cache the worktree's own path", out[0], want)
	}
	if out[1] != "submodule" {
		t.Fatalf("read %q, want the submodule's own file", out[1])
	}
}

func TestLintSlotConfigureIn_RefusesToLeaveTheLease(t *testing.T) {
	wt := t.TempDir()
	useWorkDir(t, wt)
	slot := acquireFor(t, lintSlotTool(t), wt)

	cmd := slot.ConfigureIn(sparkwing.Bash(context.Background(), `echo "$PWD"`), "../../elsewhere", "TOOL_CACHE")
	out, err := cmd.Lines()
	if err != nil {
		t.Fatalf("run with an escaping rel: %v", err)
	}
	if out[0] != slot.Path {
		t.Fatalf("an escaping rel put the command at %q, outside the lease %q", out[0], slot.Path)
	}
}

const lintSlotGetwdProbeEnv = "SPARKWING_LINTSLOT_GETWD_PROBE"

func TestLintSlot_GetwdPrefersPWDOverTheResolvedPath(t *testing.T) {
	if os.Getenv(lintSlotGetwdProbeEnv) != "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Println("getwd-error " + err.Error())
			os.Exit(1)
		}
		fmt.Println(wd)
		os.Exit(0)
	}

	wt := t.TempDir()
	useWorkDir(t, wt)
	slot := acquireFor(t, lintSlotTool(t), wt)
	if !slot.Canonical {
		t.Fatalf("could not lease a canonical slot (%+v), so the mechanism under test "+
			"cannot be exercised at all", slot)
	}
	resolved := resolves(t, wt)
	if slot.Path == resolved {
		t.Fatalf("slot path and worktree are the same directory (%q), so this test "+
			"cannot tell $PWD from a resolved path", resolved)
	}

	withPWD := runGetwdProbe(t, slot.Path, slot.Path)
	if withPWD != slot.Path {
		t.Fatalf("with PWD=%q a process placed in the slot reported %q. Go no longer "+
			"prefers $PWD, so LintSlot.Configure cannot make the linter see the slot "+
			"and every shared cache goes back to holding foreign paths",
			slot.Path, withPWD)
	}

	withoutPWD := runGetwdProbe(t, slot.Path, "")
	if withoutPWD == slot.Path {
		t.Fatalf("a process with no PWD also reported the slot path %q, so the "+
			"assertion above is vacuous -- this fixture is not exercising a symlink",
			slot.Path)
	}
	if withoutPWD != resolved {
		t.Fatalf("with no PWD the probe reported %q, want the resolved worktree %q",
			withoutPWD, resolved)
	}
}

func runGetwdProbe(t *testing.T, dir, pwd string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLintSlot_GetwdPrefersPWDOverTheResolvedPath$")
	cmd.Dir = dir
	env := []string{lintSlotGetwdProbeEnv + "=1"}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PWD=") || strings.HasPrefix(kv, lintSlotGetwdProbeEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	if pwd != "" {
		env = append(env, "PWD="+pwd)
	}
	cmd.Env = env

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("getwd probe in %q: %v (%s)", dir, err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		t.Fatalf("getwd probe in %q printed nothing", dir)
	}
	return lines[len(lines)-1]
}

func TestLintSlotConfigure_RunsInsideTheHoldersTree(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "marker.txt"), []byte("holder"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	useWorkDir(t, wt)
	slot := acquireFor(t, lintSlotTool(t), wt)

	got, err := slot.Configure(sparkwing.Bash(context.Background(), "cat marker.txt"), "TOOL_CACHE").String()
	if err != nil {
		t.Fatalf("cat through slot: %v", err)
	}
	if got != "holder" {
		t.Fatalf("read %q through the slot, want the holder's file", got)
	}
}
