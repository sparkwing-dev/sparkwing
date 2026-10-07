//go:build windows

package fssecure_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestSecurePrivateDirAppliesProtectedCurrentUserDACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	assertProtectedCurrentUserDACL(t, root)
}

func assertProtectedCurrentUserDACL(t *testing.T, path string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	dacl, present, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if !present || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("private DACL present=%v acl=%+v", present, dacl)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sddl := descriptor.String()
	if !strings.Contains(sddl, "D:P") || !strings.Contains(sddl, user.User.Sid.String()) {
		t.Fatalf("private directory DACL = %q", sddl)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("private directory DACL control = %#x, want SE_DACL_PROTECTED", control)
	}
}
