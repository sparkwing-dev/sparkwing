//go:build windows

package fssecure

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openCredentialFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }

func verifyCredentialFile(file *os.File, _ os.FileInfo, _ bool) error {
	const writeAccess = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER
	return foreignAccess(file, writeAccess, "writable")
}

// verifyOwnerOnly refuses a private key that any principal besides the owner,
// SYSTEM or Administrators may read.
func verifyOwnerOnly(file *os.File, _ os.FileInfo) error {
	const readAccess = windows.GENERIC_ALL | windows.GENERIC_READ | windows.FILE_READ_DATA
	return foreignAccess(file, readAccess, "readable")
}

func foreignAccess(file *os.File, mask uint32, verb string) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	current, system, admins, err := privateConfigSIDs()
	if err != nil {
		return err
	}
	if owner == nil || (!owner.Equals(current) && !owner.Equals(system) && !owner.Equals(admins)) {
		return errors.New("credential owner is not the current user or a system administrator")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("credential has no access list")
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsupported credential access entry type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uint32(ace.Mask)&mask != 0 && !sid.Equals(current) && !sid.Equals(system) && !sid.Equals(admins) {
			return fmt.Errorf("credential is %s by another principal", verb)
		}
	}
	return nil
}
