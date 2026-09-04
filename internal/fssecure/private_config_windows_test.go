//go:build windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSecurePrivateDirRestrictsInheritedWindowsAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(root, DirMode); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "bundle")
	if err := os.WriteFile(child, []byte("private"), FileMode); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(child, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	current, system, admins, err := privateConfigSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(current) {
		t.Fatal("snapshot child owner is not the current user")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("snapshot child DACL: %v", err)
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("unsupported access entry type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(current) && !sid.Equals(system) && !sid.Equals(admins) {
			t.Fatalf("snapshot child grants access to %s", sid.String())
		}
	}
}

func TestOpenPrivateConfigEnforcesProtectedWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("token: private\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	f, err := OpenPrivateConfig(path)
	if err != nil {
		t.Fatalf("open protected file: %v", err)
	}
	_ = f.Close()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	inheritedDACL, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, inheritedDACL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivateConfig(path); err == nil {
		t.Fatal("agent config with an inheritable access list was accepted")
	}
	if err := SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}

	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivateConfig(path); err == nil {
		t.Fatal("agent config with Everyone read access was accepted")
	}
}
