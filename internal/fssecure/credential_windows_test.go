//go:build windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCredentialWindowsAccess(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := WriteFile(path, []byte("credential")); err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	current, system, admins, err := privateConfigSIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, writable := range []bool{false, true} {
		var entries []windows.EXPLICIT_ACCESS
		for _, sid := range []*windows.SID{current, system, admins} {
			entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
		}
		access := windows.ACCESS_MASK(windows.GENERIC_READ)
		if writable {
			access |= windows.GENERIC_WRITE
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: access, AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(everyone)}})
		acl, err := windows.ACLFromEntries(entries, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
		f, err := OpenCredential(directory, "token")
		if f != nil {
			_ = f.Close()
		}
		if (err != nil) != writable {
			t.Fatalf("Everyone writable=%t: %v", writable, err)
		}
	}
	if err := SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenCredential(directory, "folder"); err == nil {
		_ = f.Close()
		t.Fatal("directory accepted as credential")
	}
}
