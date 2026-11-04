package wingd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/originguard"
)

const dashboardMemoryFile = "dashboard.json"

type dashboardMemory struct {
	Address  string `json:"address"`
	ReadOnly bool   `json:"read_only"`
}

func (r *localRuntime) rememberDashboard(memory dashboardMemory) error {
	body, err := json.Marshal(memory)
	if err != nil {
		return err
	}
	return fssecure.WriteFile(filepath.Join(r.layout.dir, dashboardMemoryFile), body)
}

func (r *localRuntime) rememberedDashboard() (dashboardMemory, bool, error) {
	body, err := os.ReadFile(filepath.Join(r.layout.dir, dashboardMemoryFile))
	if errors.Is(err, os.ErrNotExist) {
		return dashboardMemory{}, false, nil
	}
	if err != nil {
		return dashboardMemory{}, false, err
	}
	var memory dashboardMemory
	if err := json.Unmarshal(body, &memory); err != nil || memory.Address == "" {
		return dashboardMemory{}, false, fmt.Errorf("wingd: remembered dashboard %s is unreadable", dashboardMemoryFile)
	}
	// --allow-remote consents for one explicit start, so a restore never reopens a remote listener.
	if !originguard.LoopbackHost(memory.Address) {
		_, port, err := net.SplitHostPort(memory.Address)
		if err != nil {
			return dashboardMemory{}, false, fmt.Errorf("wingd: remembered dashboard %s is unreadable", dashboardMemoryFile)
		}
		memory.Address = net.JoinHostPort("127.0.0.1", port)
	}
	return memory, true, nil
}

// ForgetDashboard clears the dashboard a controller restarted on demand would serve again.
// An explicit stop calls it, so only an idle exit or a crash brings the dashboard back.
func ForgetDashboard(home string) error {
	home, err := HomeDir(home)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(home, "wingd", dashboardMemoryFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
