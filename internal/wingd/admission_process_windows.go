package wingd

import (
	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func admissionProcessIdentity(pid int) (localhostwire.LocalPeerIdentity, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	return localhostwire.LocalPeerIdentity{GOOS: "windows", SID: user.User.Sid.String()}, nil
}
