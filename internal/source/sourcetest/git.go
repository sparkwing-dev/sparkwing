// Package sourcetest supplies isolated native-git fixtures for source acceptance.
package sourcetest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Remote redirects git fetches to local fixtures and records argv, env and private file paths.
func Remote(t *testing.T, repositories map[string]string, lfsEndpoint ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable shim; Windows source support requires Windows CI")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "commands.jsonl")
	if lfsEndpoint == nil {
		lfsEndpoint = []string{}
	}
	encode := func(value any) string { b, _ := json.Marshal(value); return string(b) }
	program := "#!" + python + "\n" + `import json, os, shlex, stat, subprocess, sys
args = sys.argv[1:]
config = [x.split('=', 1)[1] for x in args if x.startswith('include.path=')]
ssh = shlex.split(os.environ.get('GIT_SSH_COMMAND', ''))
if '-i' in ssh:
    config.append(ssh[ssh.index('-i') + 1])
    config.extend(x.split('=', 1)[1] for x in ssh if x.startswith('UserKnownHostsFile='))
record = {'args': args, 'env': dict(os.environ), 'files': config}
for name in config:
    if stat.S_IMODE(os.stat(name).st_mode) != 0o600:
        raise RuntimeError('credential file is not private')
with open(` + encode(log) + `, 'a') as out:
    out.write(json.dumps(record) + '\n')
mapping = ` + encode(repositories) + `
lfs_endpoint = ` + encode(lfsEndpoint) + `
if 'lfs' in args and lfs_endpoint:
    if 'fetch' in args:
        if 'lfs.url=' + lfs_endpoint[0] not in args:
            sys.exit(2)
        if lfs_endpoint[0].startswith(('ssh://', 'git@')) and '-i' not in ssh:
            sys.exit(3)
        if lfs_endpoint[0].startswith('https://'):
            auth = subprocess.run([` + encode(realGit) + `] + args[:args.index('lfs')] + ['config', '--get-urlmatch', 'http.extraheader', lfs_endpoint[0]], capture_output=True)
            if auth.returncode != 0 or not auth.stdout:
                sys.exit(4)
    elif 'checkout' in args:
        with open(os.path.join(args[args.index('-C') + 1], 'lfs.bin'), 'w') as out:
            out.write('downloaded LFS content')
    sys.exit(0)
if 'fetch' in args or 'ls-remote' in args:
    args = [mapping.get(x, x) for x in args]
    os.environ['GIT_ALLOW_PROTOCOL'] = 'file'
os.execv(` + encode(realGit) + `, [` + encode(realGit) + `] + args)
`
	name := filepath.Join(bin, "git")
	if err := os.WriteFile(name, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// Environment gives source tools an isolated home and the fixture git executable.
func Environment(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
}

// Git runs native fixture operations without the user's git configuration.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(Environment(t), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Repository creates a committed pipeline checkout and returns its exact commit.
func Repository(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, "init", "--initial-branch=trunk")
	Git(t, dir, "config", "user.email", "source@example.test")
	Git(t, dir, "config", "user.name", "Source Test")
	if err := os.Mkdir(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main\n")
	Git(t, dir, "add", ".")
	Git(t, dir, "commit", "-qm", "first")
	Git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	return dir, Git(t, dir, "rev-parse", "HEAD")
}

// Write stores fixture content in an owner-only file.
func Write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
