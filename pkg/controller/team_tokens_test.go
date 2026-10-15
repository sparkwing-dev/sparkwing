package controller_test

import "github.com/sparkwing-dev/sparkwing/pkg/store"

var (
	machineScopes = store.MachineTokenScopes()
	memberScopes  = store.CLITokenScopes(store.RoleEditor)
)

type mintedRunner struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}
