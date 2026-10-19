package charts

import (
	"encoding/json"
	"os"
	"path"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

// The Docker sidecar's filter stacks over every container a customer starts,
// so it must keep what nested runc needs and refuse the user-namespace kernel
// surface Docker's default profile also refuses.
func TestDindSeccompProfile(t *testing.T) {
	file := "sparkwing-runner-bundle/files/seccomp/" + path.Base(launcher.DockerSeccompProfile)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		DefaultAction   string   `json:"defaultAction"`
		DefaultErrnoRet *int     `json:"defaultErrnoRet"`
		Architectures   []string `json:"architectures"`
		Syscalls        []struct {
			Names    []string `json:"names"`
			Action   string   `json:"action"`
			ErrnoRet *int     `json:"errnoRet"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.DefaultAction != "SCMP_ACT_ERRNO" || !slices.Contains(profile.Architectures, "SCMP_ARCH_X86_64") || !slices.Contains(profile.Architectures, "SCMP_ARCH_AARCH64") {
		t.Fatalf("profile must deny by default on amd64 and arm64: %s %v", profile.DefaultAction, profile.Architectures)
	}
	// safety: a name may have several argument-filtered rules; any ALLOW wins.
	action := map[string]string{}
	errno := map[string]int{}
	for _, rule := range profile.Syscalls {
		for _, name := range rule.Names {
			if action[name] != "SCMP_ACT_ALLOW" {
				action[name] = rule.Action
			}
			if rule.ErrnoRet != nil {
				errno[name] = *rule.ErrnoRet
			}
		}
	}
	for _, name := range []string{"mount", "umount2", "unshare", "setns", "pivot_root", "clone", "open_tree", "move_mount", "mount_setattr", "fsopen", "fsconfig", "fsmount", "sethostname", "chroot", "ptrace", "seccomp", "capset", "mknodat"} {
		if action[name] != "SCMP_ACT_ALLOW" {
			t.Errorf("dind needs %s", name)
		}
	}
	for _, name := range []string{"io_uring_setup", "io_uring_enter", "io_uring_register", "keyctl", "add_key", "request_key", "clone3"} {
		if action[name] != "SCMP_ACT_ERRNO" || errno[name] != 38 {
			t.Errorf("%s must fail with ENOSYS so runtimes fall back", name)
		}
	}
	for _, name := range []string{"bpf", "perf_event_open", "userfaultfd", "kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module", "open_by_handle_at", "fanotify_init"} {
		if action[name] != "SCMP_ACT_ERRNO" {
			t.Errorf("%s must be refused", name)
		}
	}
}
