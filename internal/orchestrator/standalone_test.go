package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestStandaloneReasonFor_ClassifiesEachBranch(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", wingdclient.ErrNoDaemonHost), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonHostUnusable), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonHostFailed), standaloneNoDaemon},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonTooOld), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonLacksOperation), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", client.ErrControllerLacksRoute), standaloneDaemonOlder},
		{fmt.Errorf("x: %w", wingdclient.ErrProtocolTooOld), standaloneFloor},
		{fmt.Errorf("x: %w", wingdclient.ErrDaemonUnreachable), ""},
		{fmt.Errorf("x: %w", wingdclient.ErrBuildMismatch), ""},
		{errors.New("disk is on fire"), ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := standaloneReasonFor(tc.err); got != tc.want {
			t.Errorf("standaloneReasonFor(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
