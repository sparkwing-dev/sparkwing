//go:build windows

package source_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestPublicationCopyRejectsInheritedPublicAccess(t *testing.T) {
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
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", parent)
	t.Setenv("TEMP", parent)
	repo, _ := sourcetest.Repository(t)
	path := filepath.Join(t.TempDir(), "source.bundle")
	sourcetest.Git(t, repo, "bundle", "create", path, "HEAD")
	stream, cleanup, err := source.OpenPublication(t.Context(), source.Publication{
		Path:    path,
		Base:    procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()},
		Secrets: map[string]string{"SIGNING_MATERIAL": "withheld-but-absent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{filepath.Dir(stream.Name()), stream.Name()} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := fssecure.VerifyPrivateConfig(name, info); err != nil {
			t.Errorf("publication copy exposes %s: %v", name, err)
		}
	}
	if n, err := io.Copy(io.Discard, stream); err != nil || n == 0 {
		t.Fatalf("private upload stream: bytes=%d err=%v", n, err)
	}
}
