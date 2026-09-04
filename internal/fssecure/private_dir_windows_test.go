//go:build windows

package fssecure_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"golang.org/x/sys/windows"
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

func TestMkdirPrivateTempDoesNotInheritAPermissiveParentDACL(t *testing.T) {
	parent := t.TempDir()
	everyone, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		parent,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	directory, err := fssecure.MkdirPrivateTemp(parent, "probe-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(directory) }()
	assertProtectedCurrentUserDACL(t, directory)
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
